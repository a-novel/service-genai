package core

import (
	"context"
	"fmt"
	"time"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
)

// GenerationSweepDao takes the generations nobody checked within the interval.
type GenerationSweepDao interface {
	Exec(ctx context.Context, request *dao.GenerationSweepRequest) ([]*dao.Generation, error)
}

// GenerationSweepServiceCheck checks one generation.
type GenerationSweepServiceCheck interface {
	Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
}

// GenerationSweepConfig is what a [GenerationSweep] needs to run.
type GenerationSweepConfig struct {
	// Interval is how long a check stays fresh, and how often the sweep runs.
	Interval time.Duration
	// BatchSize caps the generations checked in one pass.
	BatchSize int
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
	config       GenerationSweepConfig
	dao          GenerationSweepDao
	serviceCheck GenerationSweepServiceCheck
}

// NewGenerationSweep returns a new GenerationSweep service.
func NewGenerationSweep(
	config GenerationSweepConfig, dao GenerationSweepDao, serviceCheck GenerationSweepServiceCheck,
) *GenerationSweep {
	return &GenerationSweep{config: config, dao: dao, serviceCheck: serviceCheck}
}

// Exec checks one batch.
func (service *GenerationSweep) Exec(
	ctx context.Context, _ *GenerationSweepRequest,
) (*GenerationSweepResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep")
	defer span.End()

	generations, err := service.dao.Exec(ctx, &dao.GenerationSweepRequest{
		Interval: service.config.Interval, Limit: service.config.BatchSize,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("sweep generations: %w", err))
	}

	for _, generation := range generations {
		if ctx.Err() != nil {
			break
		}

		service.checkOne(ctx, generation)
	}

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
