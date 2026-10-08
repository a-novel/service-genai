package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
)

// Dependencies of [GenerationCancel].
type (
	// GenerationCancelDao cancels or marks the generation.
	GenerationCancelDao interface {
		Exec(ctx context.Context, request *dao.GenerationRequestCancelRequest) (*dao.Generation, error)
	}
	// GenerationCancelGetDao reads a generation that could not be stopped.
	GenerationCancelGetDao interface {
		Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
	}
	// GenerationCancelUsageListDao reads what the generation consumed.
	GenerationCancelUsageListDao interface {
		Exec(ctx context.Context, request *dao.GenerationUsageListRequest) ([]*dao.GenerationUsage, error)
	}
	// GenerationCancelServiceCheck stops a running provider call.
	GenerationCancelServiceCheck interface {
		Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
	}
)

// GenerationCancelConfig is what a [GenerationCancel] needs to run.
type GenerationCancelConfig struct {
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration `validate:"required"`
}

// GenerationCancelRequest holds the parameters for a [GenerationCancel.Exec] call.
type GenerationCancelRequest struct {
	ID      uuid.UUID `validate:"required"`
	OwnerID uuid.UUID `validate:"required"`
}

// A GenerationCancel stops a generation.
//
// One that never started is settled at once. One whose provider call is known is checked right
// away, which cancels the call and settles it with whatever it consumed: a cancelled call is not a
// free one. One whose start is in flight is only marked, and the next check stops it. One already
// settled is returned as it stands, so a cancel that lost a race or is retried still succeeds.
type GenerationCancel struct {
	config   GenerationCancelConfig
	dao      GenerationCancelDao
	getDao   GenerationCancelGetDao
	usageDao GenerationCancelUsageListDao
	check    GenerationCancelServiceCheck
}

func NewGenerationCancel(
	config GenerationCancelConfig,
	cancelDao GenerationCancelDao,
	getDao GenerationCancelGetDao,
	usageDao GenerationCancelUsageListDao,
	check GenerationCancelServiceCheck,
) (*GenerationCancel, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	return &GenerationCancel{config: config, dao: cancelDao, getDao: getDao, usageDao: usageDao, check: check}, nil
}

func (service *GenerationCancel) Exec(
	ctx context.Context, request *GenerationCancelRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCancel")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.owner_id", request.OwnerID.String()),
	)

	err := validate.Struct(request)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrInvalidRequest, err))
	}

	generation, err := service.dao.Exec(ctx, &dao.GenerationRequestCancelRequest{
		ID:        request.ID,
		OwnerID:   request.OwnerID,
		Error:     generationCancelledReason,
		Retention: service.config.Retention,
	})

	if errors.Is(err, dao.ErrGenerationNotCancellable) {
		// Settled already, or not this owner's. The read is owner-scoped, so it tells the two apart
		// without revealing anybody else's generation.
		generation, err = service.getDao.Exec(ctx, &dao.GenerationGetRequest{ID: request.ID, OwnerID: request.OwnerID})
		if errors.Is(err, dao.ErrGenerationGetNotFound) {
			return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrGenerationNotFound, err))
		}
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("cancel generation: %w", err))
	}

	if generation.ProviderCallID != nil && generation.SettledAt == nil {
		generation, err = service.check.Exec(ctx, &GenerationCheckRequest{Generation: generation})
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("stop provider call: %w", err))
		}
	}

	usage, err := service.usageDao.Exec(ctx, &dao.GenerationUsageListRequest{GenerationID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("list usage: %w", err))
	}

	return newGeneration(generation, usage), nil
}
