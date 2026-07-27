package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
)

// GenerationCancelDao is the data-access dependency of [GenerationCancel].
type GenerationCancelDao interface {
	Exec(ctx context.Context, request *dao.GenerationRequestCancelRequest) (*dao.Generation, error)
}

// GenerationCancelRequest holds the parameters for a [GenerationCancel.Exec] call.
type GenerationCancelRequest struct {
	ID      uuid.UUID `validate:"required"`
	OwnerID uuid.UUID `validate:"required"`
}

// A GenerationCancel asks for a generation to be stopped.
//
// It marks the request and returns; it does not stop anything itself. The worker holding the
// generation observes the mark at its next poll, cancels the provider operation, and settles —
// recording whatever was consumed before the stop, because a cancelled call is not a free one.
type GenerationCancel struct {
	dao GenerationCancelDao
}

func NewGenerationCancel(cancelDao GenerationCancelDao) *GenerationCancel {
	return &GenerationCancel{dao: cancelDao}
}

func (service *GenerationCancel) Exec(
	ctx context.Context, request *GenerationCancelRequest,
) (*dao.Generation, error) {
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
		ID: request.ID, OwnerID: request.OwnerID,
	})

	if errors.Is(err, dao.ErrGenerationNotCancellable) {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrGenerationNotCancellable, err))
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("cancel generation: %w", err))
	}

	return otel.ReportSuccess(span, generation), nil
}
