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

// GenerationGetDao reads the generation.
type GenerationGetDao interface {
	Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
}

// GenerationGetDaoAttemptList reads the provider's answers to the generation's attempts.
type GenerationGetDaoAttemptList interface {
	Exec(ctx context.Context, request *dao.GenerationAttemptListRequest) ([]*dao.GenerationAttempt, error)
}

// GenerationGetServiceCheck checks the generation.
type GenerationGetServiceCheck interface {
	Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
}

// GenerationGetRequest holds the parameters for a [GenerationGet.Exec] call.
type GenerationGetRequest struct {
	// ID identifies the generation to read.
	ID uuid.UUID `validate:"required"`
	// OwnerID scopes the read, and is not optional: it is the whole ownership predicate.
	OwnerID uuid.UUID `validate:"required"`
}

// A GenerationGet reads one of an owner's generations, checking it with the provider first while it
// is unsettled.
//
// Every poll of an unsettled generation asks the provider, so a poll made after the provider finished
// returns the settled generation. A check writes only to settle, so polling costs the row nothing. The
// caller bounds how often it polls, and the sweep checks the generations nobody polls.
type GenerationGet struct {
	dao            GenerationGetDao
	daoAttemptList GenerationGetDaoAttemptList
	serviceCheck   GenerationGetServiceCheck
}

// NewGenerationGet returns a new GenerationGet service.
func NewGenerationGet(
	dao GenerationGetDao, daoAttemptList GenerationGetDaoAttemptList, serviceCheck GenerationGetServiceCheck,
) *GenerationGet {
	return &GenerationGet{dao: dao, daoAttemptList: daoAttemptList, serviceCheck: serviceCheck}
}

func (service *GenerationGet) Exec(ctx context.Context, request *GenerationGetRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationGet")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.owner_id", request.OwnerID.String()),
	)

	err := validate.Struct(request)
	if err != nil {
		return nil, otel.ReportError(span, errors.Join(err, ErrInvalidRequest))
	}

	generation, err := service.dao.Exec(ctx, &dao.GenerationGetRequest{ID: request.ID, OwnerID: request.OwnerID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get generation: %w", err))
	}

	// A check that failed, such as on a provider hiccup, is redone by the next poll or the sweep: the
	// caller gets the generation as read. The check's own span holds the failure.
	if generation.SettledAt == nil {
		checked, checkErr := service.serviceCheck.Exec(ctx, &GenerationCheckRequest{Generation: generation})
		if checkErr == nil {
			generation = checked
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
