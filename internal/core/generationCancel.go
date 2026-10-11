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

// GenerationCancelDao cancels or marks the generation.
type GenerationCancelDao interface {
	Exec(ctx context.Context, request *dao.GenerationRequestCancelRequest) (*dao.Generation, error)
}

// GenerationCancelDaoGet reads a generation that could not be stopped.
type GenerationCancelDaoGet interface {
	Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
}

// GenerationCancelDaoAttemptList reads the provider's answers to the generation's attempts.
type GenerationCancelDaoAttemptList interface {
	Exec(ctx context.Context, request *dao.GenerationAttemptListRequest) ([]*dao.GenerationAttempt, error)
}

// GenerationCancelServiceCheck stops a running provider call.
type GenerationCancelServiceCheck interface {
	Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
}

// GenerationCancelConfig is what a [GenerationCancel] needs to run.
type GenerationCancelConfig struct {
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration
}

// GenerationCancelRequest holds the parameters for a [GenerationCancel.Exec] call.
type GenerationCancelRequest struct {
	// ID identifies the generation to stop.
	ID uuid.UUID `validate:"required"`
	// OwnerID scopes the cancel to the owner's generations.
	OwnerID uuid.UUID `validate:"required"`
}

// A GenerationCancel stops a generation.
//
// One that never started is settled at once. One whose provider call is known is checked right
// away, which cancels the call and settles it with the provider's answer: a cancelled call is not a
// free one. One whose start is in flight is only marked, and the next check stops it. One already
// settled is returned as it stands, so a cancel that lost a race or is retried still succeeds.
type GenerationCancel struct {
	config         GenerationCancelConfig
	dao            GenerationCancelDao
	daoGet         GenerationCancelDaoGet
	daoAttemptList GenerationCancelDaoAttemptList
	serviceCheck   GenerationCancelServiceCheck
}

// NewGenerationCancel returns a new GenerationCancel service.
func NewGenerationCancel(
	config GenerationCancelConfig,
	dao GenerationCancelDao,
	daoGet GenerationCancelDaoGet,
	daoAttemptList GenerationCancelDaoAttemptList,
	serviceCheck GenerationCancelServiceCheck,
) *GenerationCancel {
	return &GenerationCancel{
		config: config, dao: dao, daoGet: daoGet, daoAttemptList: daoAttemptList, serviceCheck: serviceCheck,
	}
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
		return nil, otel.ReportError(span, errors.Join(err, ErrInvalidRequest))
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
		generation, err = service.daoGet.Exec(ctx, &dao.GenerationGetRequest{ID: request.ID, OwnerID: request.OwnerID})
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("cancel generation: %w", err))
	}

	if generation.ProviderCallID != nil && generation.SettledAt == nil {
		generation, err = service.serviceCheck.Exec(ctx, &GenerationCheckRequest{Generation: generation})
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("stop provider call: %w", err))
		}
	}

	var attempts []*dao.GenerationAttempt

	if mayHaveAttempts(generation) {
		attempts, err = service.daoAttemptList.Exec(ctx, &dao.GenerationAttemptListRequest{GenerationID: generation.ID})
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("list attempts: %w", err))
		}
	}

	return newGeneration(generation, attempts), nil
}
