package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
)

// Dependencies of [GenerationSweep].
type (
	// GenerationSweepDao takes the generations nobody checked within the interval.
	GenerationSweepDao interface {
		Exec(ctx context.Context, request *dao.GenerationSweepRequest) ([]*dao.Generation, error)
	}
	// GenerationSweepServiceCheck checks one generation.
	GenerationSweepServiceCheck interface {
		Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
	}
)

// GenerationSweepConfig is what a [GenerationSweep] needs to run.
type GenerationSweepConfig struct {
	// Interval is how long a check stays fresh, and how often the sweep runs.
	Interval time.Duration `validate:"required,gt=0"`
	// BatchSize caps the generations checked in one pass.
	BatchSize int `validate:"required,min=1,max=100"`
	// ProviderEpoch is this replica's provider configuration. Generations a newer one took over are
	// left to the replicas running it.
	ProviderEpoch int32 `validate:"required,min=1"`
}

// A GenerationSweep checks the unsettled generations nobody checked within the interval.
//
// Callers polling a generation keep it fresh. The sweep catches the rest: a generation whose caller
// went away still settles while the provider holds its result, and one whose start never ran still
// starts.
type GenerationSweep struct {
	config GenerationSweepConfig
	dao    GenerationSweepDao
	check  GenerationSweepServiceCheck
}

func NewGenerationSweep(
	config GenerationSweepConfig, sweepDao GenerationSweepDao, check GenerationSweepServiceCheck,
) (*GenerationSweep, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	if config.Interval > SweepIntervalCeiling {
		return nil, fmt.Errorf(
			"%w: sweep interval %s exceeds %s", ErrInvalidRequest, config.Interval, SweepIntervalCeiling,
		)
	}

	return &GenerationSweep{config: config, dao: sweepDao, check: check}, nil
}

// RunOnce checks one batch, reporting whether it found any. A pass that found work runs again
// immediately, so a backlog drains at full speed.
func (service *GenerationSweep) RunOnce(ctx context.Context) (bool, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSweep")
	defer span.End()

	generations, err := service.dao.Exec(ctx, &dao.GenerationSweepRequest{
		Interval: service.config.Interval, Limit: service.config.BatchSize, ProviderEpoch: service.config.ProviderEpoch,
	})
	if err != nil {
		return false, otel.ReportError(span, fmt.Errorf("sweep generations: %w", err))
	}

	var checkErr error

	for _, generation := range generations {
		if ctx.Err() != nil {
			break
		}

		_, err = service.check.Exec(ctx, &GenerationCheckRequest{Generation: generation})
		if err != nil {
			// One generation's failure belongs to it; the rest of the batch still progresses.
			checkErr = errors.Join(checkErr, fmt.Errorf("check generation %s: %w", generation.ID, err))
		}
	}

	if checkErr != nil {
		return len(generations) > 0, otel.ReportError(span, checkErr)
	}

	return len(generations) > 0, nil
}
