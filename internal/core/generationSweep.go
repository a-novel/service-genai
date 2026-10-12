package core

import (
	"context"
	"fmt"
	"sync"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/dao"
)

// sweepConcurrency bounds the checks one pass runs at once, well under the database pool.
const sweepConcurrency = 8

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

// GenerationSweepResult reports what one pass found.
type GenerationSweepResult struct {
	// Checked is how many generations the pass took. A pass that took any runs again immediately, so
	// a backlog drains at full speed.
	Checked int
}

// A GenerationSweep checks the unsettled generations nobody checked within the interval.
//
// Callers polling a generation keep it fresh. The sweep catches the rest: a generation whose caller
// went away still settles while the provider holds its result, and one whose start never ran still
// starts.
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

// Exec checks one batch.
func (service *GenerationSweep) Exec(
	ctx context.Context, _ *GenerationSweepRequest,
) (*GenerationSweepResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep")
	defer span.End()

	generations, err := service.dao.Exec(ctx, &dao.GenerationSweepRequest{
		Interval: service.sweep.Interval, Limit: service.sweep.BatchSize,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("sweep generations: %w", err))
	}

	// Checks of different generations are independent, so a pass runs several at once: a slow
	// provider then stretches a pass by its latency, not by the batch size times it.
	var (
		checks sync.WaitGroup
		slots  = make(chan struct{}, sweepConcurrency)
	)

	for _, generation := range generations {
		if ctx.Err() != nil {
			break
		}

		slots <- struct{}{}

		checks.Go(func() {
			defer func() { <-slots }()

			service.checkOne(ctx, generation)
		})
	}

	checks.Wait()

	return &GenerationSweepResult{Checked: len(generations)}, nil
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
