package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/dao"
)

// GenerationGetDao reads the generation.
type GenerationGetDao interface {
	Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
}

// GenerationGetDaoElectCheck elects this read to check a stale generation.
type GenerationGetDaoElectCheck interface {
	Exec(ctx context.Context, request *dao.GenerationElectCheckRequest) (*dao.Generation, error)
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

// A GenerationGet reads one of an owner's generations, checking it first when it is stale.
//
// A caller's polls keep its generation fresh, and the sweep checks the generations nobody polls.
type GenerationGet struct {
	dao            GenerationGetDao
	daoElectCheck  GenerationGetDaoElectCheck
	daoAttemptList GenerationGetDaoAttemptList
	serviceCheck   GenerationGetServiceCheck
	generations    config.Generations
}

// NewGenerationGet returns a new GenerationGet service.
func NewGenerationGet(
	dao GenerationGetDao,
	daoElectCheck GenerationGetDaoElectCheck,
	daoAttemptList GenerationGetDaoAttemptList,
	serviceCheck GenerationGetServiceCheck,
	generations config.Generations,
) *GenerationGet {
	return &GenerationGet{
		dao:            dao,
		daoElectCheck:  daoElectCheck,
		daoAttemptList: daoAttemptList,
		serviceCheck:   serviceCheck,
		generations:    generations,
	}
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

	// Electing first spares a read: an elected poll gets the generation from the election itself.
	generation, err := service.daoElectCheck.Exec(ctx, &dao.GenerationElectCheckRequest{
		ID: request.ID, OwnerID: request.OwnerID, Interval: service.generations.CheckInterval,
	})

	switch {
	case err == nil:
		// A check that failed, such as on a lock a migration holds, is redone by a later poll or the
		// sweep: the caller gets the generation as elected. The check's own span holds the failure.
		checked, checkErr := service.serviceCheck.Exec(ctx, &GenerationCheckRequest{Generation: generation})
		if checkErr == nil {
			generation = checked
		}
	case errors.Is(err, dao.ErrGenerationCheckNotDue):
		// Checked within the interval, settled, or not this owner's: the read answers.
		generation, err = service.dao.Exec(ctx, &dao.GenerationGetRequest{ID: request.ID, OwnerID: request.OwnerID})
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("get generation: %w", err))
		}
	default:
		return nil, otel.ReportError(span, fmt.Errorf("elect check: %w", err))
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
