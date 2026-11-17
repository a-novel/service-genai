package core

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/dao"
)

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
// went away still settles while the provider holds its result. The sweep server's ticker calls it.
// One sweep runs at a time across every replica, the leader: the others skip their run. A check
// writes only to settle, so the sweep costs the rows nothing.
type GenerationSweep struct {
	dao          GenerationSweepDao
	daoLock      GenerationSweepDaoLock
	daoUnlock    GenerationSweepDaoUnlock
	serviceCheck GenerationSweepServiceCheck
	sweep        config.Sweep
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
	}
}

// Exec leads the run, or skips it when another sweep leads. Leading, it checks batch after batch,
// each following the last generation of the one before, while each comes back full, so one call reads
// every unsettled generation once. It starts no check once its context ends, as when the server stops.
// It answers once the checks it started finish.
func (service *GenerationSweep) Exec(
	ctx context.Context, _ *GenerationSweepRequest,
) (*GenerationSweepResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep")
	defer span.End()

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

	for ctx.Err() == nil {
		generations, err := service.sweepBatch(ctx, after)
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
// different generations are independent, so several run at once, as many as the sweep's concurrency.
// A slow provider then stretches a batch by its latency alone. A generation read but left unchecked
// waits for a later call.
func (service *GenerationSweep) sweepBatch(ctx context.Context, after uuid.UUID) ([]*dao.Generation, error) {
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

	slots := make(chan struct{}, service.sweep.Concurrency)

	for _, generation := range generations {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}

		// The server may stop while the batch waits for a slot: it starts no further check.
		if ctx.Err() != nil {
			break
		}

		checks.Go(func() {
			defer func() { <-slots }()

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
