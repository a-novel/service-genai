package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/dao"
)

// ErrSweepDeadlineTooShort is returned when the caller's deadline leaves no room for a whole check,
// so the sweep could check nothing.
var ErrSweepDeadlineTooShort = errors.New("deadline too short for a sweep")

// GenerationSweepDao takes the generations nobody checked within the interval.
type GenerationSweepDao interface {
	Exec(ctx context.Context, request *dao.GenerationSweepRequest) ([]*dao.Generation, error)
}

// GenerationSweepServiceCheck checks one generation.
type GenerationSweepServiceCheck interface {
	Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
}

// GenerationSweepRequest holds the parameters for a [GenerationSweep.Exec] call.
type GenerationSweepRequest struct{}

// GenerationSweepResult reports what one call found.
type GenerationSweepResult struct {
	// Checked is how many generations the call took.
	Checked int
}

// A GenerationSweep checks the unsettled generations nobody checked within the interval.
//
// Callers polling a generation keep it fresh. The sweep catches the rest: a generation whose caller
// went away still settles while the provider holds its result, and one whose start never ran still
// starts. A scheduler calls it, so it runs within a request, where the instance has its CPU.
type GenerationSweep struct {
	dao          GenerationSweepDao
	serviceCheck GenerationSweepServiceCheck
	sweep        config.Sweep
}

// NewGenerationSweep returns a new GenerationSweep service.
func NewGenerationSweep(
	dao GenerationSweepDao, serviceCheck GenerationSweepServiceCheck, sweep config.Sweep,
) *GenerationSweep {
	return &GenerationSweep{dao: dao, serviceCheck: serviceCheck, sweep: sweep}
}

// Exec checks one batch after another while each comes back full, so a backlog drains in one call.
// It starts no check its caller's deadline could not wait out, and none once its context ends, as
// when the server stops: it answers once the checks it started finish.
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

	result := &GenerationSweepResult{}

	for ctx.Err() == nil && mayStart(lastStart) {
		taken, err := service.sweepBatch(ctx, lastStart)
		if err != nil {
			return nil, otel.ReportError(span, err)
		}

		result.Checked += taken

		if taken < service.sweep.BatchSize {
			break
		}
	}

	return result, nil
}

// sweepBatch takes one batch and checks it. Checks of different generations are independent, so
// several run at once: a slow provider then stretches a batch by its latency, not by the batch size
// times it. A generation taken but left unchecked waits for a later call.
func (service *GenerationSweep) sweepBatch(ctx context.Context, lastStart time.Time) (int, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep(sweepBatch)")
	defer span.End()

	generations, err := service.dao.Exec(ctx, &dao.GenerationSweepRequest{
		Interval: service.sweep.Interval, Limit: service.sweep.BatchSize,
	})
	// A sweep stopped during its query checks nothing; what the query took waits for a later call.
	if err != nil && ctx.Err() != nil {
		return 0, nil
	}

	if err != nil {
		return 0, otel.ReportError(span, fmt.Errorf("sweep generations: %w", err))
	}

	var (
		checks sync.WaitGroup
		slots  = make(chan struct{}, service.sweep.Concurrency)
	)

	for _, generation := range generations {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}

		if ctx.Err() != nil || !mayStart(lastStart) {
			break
		}

		checks.Go(func() {
			defer func() { <-slots }()

			service.checkOne(ctx, generation)
		})
	}

	checks.Wait()

	return len(generations), nil
}

// checkOne keeps a failure or a panic with the generation that caused it, so one bad row neither
// ends the process nor slows the rest of the backlog. The server recovers a poll's panic the same way.
func (service *GenerationSweep) checkOne(ctx context.Context, generation *dao.Generation) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep(checkOne)")
	defer span.End()
	defer otel.RecoverPanic(ctx, span)

	_, err := service.serviceCheck.Exec(ctx, &GenerationCheckRequest{Generation: generation})
	if err != nil {
		_ = otel.ReportError(span, err)
	}
}

// mayStart reports whether a check may start now and still end before its caller's deadline, which
// lastStart precedes by the longest check. A zero lastStart sets no bound.
func mayStart(lastStart time.Time) bool {
	return lastStart.IsZero() || time.Now().Before(lastStart)
}
