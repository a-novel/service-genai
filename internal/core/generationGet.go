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

// GenerationGetDao is the data-access dependency of [GenerationGet].
type GenerationGetDao interface {
	Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
}

// GenerationGetRequest holds the parameters for a [GenerationGet.Exec] call.
type GenerationGetRequest struct {
	ID uuid.UUID `validate:"required"`
	// OwnerID scopes the read, and is not optional: it is the whole ownership predicate.
	OwnerID uuid.UUID `validate:"required"`
}

// A GenerationGet reads one of an owner's generations.
type GenerationGet struct {
	dao GenerationGetDao
}

func NewGenerationGet(getDao GenerationGetDao) *GenerationGet {
	return &GenerationGet{dao: getDao}
}

func (service *GenerationGet) Exec(ctx context.Context, request *GenerationGetRequest) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationGet")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.owner_id", request.OwnerID.String()),
	)

	err := validate.Struct(request)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrInvalidRequest, err))
	}

	generation, err := service.dao.Exec(ctx, &dao.GenerationGetRequest{
		ID: request.ID, OwnerID: request.OwnerID,
	})

	if errors.Is(err, dao.ErrGenerationGetNotFound) {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrGenerationNotFound, err))
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get generation: %w", err))
	}

	return otel.ReportSuccess(span, generation), nil
}
