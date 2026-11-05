package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/dao"
)

// ErrSweepDeadlineTooShort is returned when the caller's deadline leaves no room for a whole check,
// so the sweep could check nothing.
var ErrSweepDeadlineTooShort = errors.New("deadline too short for a sweep")

// GenerationSweepDao reads one page of the unsettled generations.
type GenerationSweepDao interface {
	Exec(ctx context.Context, request *dao.GenerationSweepRequest) ([]*dao.Generation, error)
}

// GenerationSweepDaoLock takes the sweep's leader role.
type GenerationSweepDaoLock interface {
	Exec(ctx context.Context, request *dao.GenerationSweepLockRequest) (*dao.GenerationSweepLock, error)
}

// GenerationSweepDaoUnlock releases the sweep's leader role.
type GenerationSweepDaoUnlock interface {
	Exec(ctx context.Context, request *dao.GenerationSweepUnlockRequest) error
}

// GenerationSweepServiceCheck checks one generation.
type GenerationSweepServiceCheck interface {
	Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
}

// GenerationSweepRequest holds the parameters for a [GenerationSweep.Exec] call.
type GenerationSweepRequest struct{}

// GenerationSweepResult reports what one call found.
type GenerationSweepResult struct {
	// Checked is how many generations the call read. Zero when another sweep led the run.
	Checked int
}

// A GenerationSweep checks every unsettled generation once.
//
// Callers polling a generation keep it fresh. The sweep catches the rest: a generation whose caller
// went away still settles while the provider holds its result. A scheduler's request or the sweep
// server's own ticker calls it. One sweep runs at a time across every replica, the leader: the others
// skip their run. A check writes only to settle, so the sweep costs the rows nothing. Calls that
// overlap in one process share its concurrency, which the connection pool sizes.
type GenerationSweep struct {
	dao          GenerationSweepDao
	daoLock      GenerationSweepDaoLock
	daoUnlock    GenerationSweepDaoUnlock
	serviceCheck GenerationSweepServiceCheck
	sweep        config.Sweep
	slots        chan struct{}
}

// NewGenerationSweep returns a new GenerationSweep service.
func NewGenerationSweep(
	dao GenerationSweepDao,
	daoLock GenerationSweepDaoLock,
	daoUnlock GenerationSweepDaoUnlock,
	serviceCheck GenerationSweepServiceCheck,
	sweep config.Sweep,
) *GenerationSweep {
	return &GenerationSweep{
		dao:          dao,
		daoLock:      daoLock,
		daoUnlock:    daoUnlock,
		serviceCheck: serviceCheck,
		sweep:        sweep,
		slots:        make(chan struct{}, sweep.Concurrency),
	}
}

// Exec leads the run, or skips it when another sweep leads. Leading, it checks batch after batch,
// each following the last generation of the one before, while each comes back full, so one call reads
// every unsettled generation once. It starts no check that could outlast its caller's deadline, and
// none once its context ends, as when the server stops. It answers once the checks it started finish.
func (service *GenerationSweep) Exec(
	ctx context.Context, _ *GenerationSweepRequest,
) (*GenerationSweepResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep")
	defer span.End()

	// A check started past lastStart could outlast the caller's deadline, and its answer with it.
	var lastStart time.Time
	if deadline, ok := ctx.Deadline(); ok {
		lastStart = deadline.Add(-checkTimeout)
	}

	if !mayStart(lastStart) {
		return nil, otel.ReportError(span, ErrSweepDeadlineTooShort)
	}

	lock, err := service.daoLock.Exec(ctx, &dao.GenerationSweepLockRequest{})
	if errors.Is(err, dao.ErrGenerationSweepLocked) {
		// The leader checks every generation this run would.
		return &GenerationSweepResult{}, nil
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("take the leader role: %w", err))
	}

	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
		defer cancel()

		// A session that cannot release the role ends, which releases it too. The unlock's own span
		// holds the failure.
		_ = service.daoUnlock.Exec(unlockCtx, &dao.GenerationSweepUnlockRequest{Lock: lock})
	}()

	result := &GenerationSweepResult{}

	var after uuid.UUID

	for ctx.Err() == nil && mayStart(lastStart) {
		generations, err := service.sweepBatch(ctx, lastStart, after)
		if err != nil {
			return nil, otel.ReportError(span, err)
		}

		result.Checked += len(generations)

		if len(generations) < service.sweep.BatchSize {
			break
		}

		after = generations[len(generations)-1].ID
	}

	return result, nil
}

// sweepBatch reads the batch after the given generation, checks it, and returns it. Checks of
// different generations are independent, so several run at once. A slow provider then stretches a
// batch by its latency alone. A generation read but left unchecked waits for a later call.
func (service *GenerationSweep) sweepBatch(
	ctx context.Context, lastStart time.Time, after uuid.UUID,
) ([]*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep(sweepBatch)")
	defer span.End()

	generations, err := service.dao.Exec(ctx, &dao.GenerationSweepRequest{
		After: after, Limit: service.sweep.BatchSize,
	})
	// A sweep stopped during its query checks nothing; what the query read waits for a later call.
	if err != nil && ctx.Err() != nil {
		return nil, nil
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("sweep generations: %w", err))
	}

	var checks sync.WaitGroup

dispatch:
	for _, generation := range generations {
		select {
		case service.slots <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}

		// The wait for a slot may outlast the deadline or the server: the slot goes back to the calls
		// sharing it.
		if ctx.Err() != nil || !mayStart(lastStart) {
			<-service.slots

			break dispatch
		}

		checks.Go(func() {
			defer func() { <-service.slots }()

			service.checkOne(ctx, generation)
		})
	}

	checks.Wait()

	return generations, nil
}

// checkOne keeps a failure or a panic with the generation that caused it, so one bad row neither
// ends the process nor slows the rest of the backlog. The server recovers a poll's panic the same way.
func (service *GenerationSweep) checkOne(ctx context.Context, generation *dao.Generation) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep(checkOne)")
	defer span.End()
	defer otel.RecoverPanic(ctx, span)

	// A failed check leaves the batch running; its own span holds the failure.
	_, _ = service.serviceCheck.Exec(ctx, &GenerationCheckRequest{Generation: generation})
}

// mayStart reports whether a check may start now and still end before its caller's deadline, which
// lastStart precedes by the longest check. A zero lastStart sets no bound.
func mayStart(lastStart time.Time) bool {
	return lastStart.IsZero() || time.Now().Before(lastStart)
}
