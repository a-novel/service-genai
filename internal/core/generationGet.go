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

// GenerationGetConfig is what a [GenerationGet] needs to run.
type GenerationGetConfig struct {
	// CheckInterval is how long a check stays fresh. However often callers poll, a generation reaches
	// the provider at most once per interval.
	CheckInterval time.Duration
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
// A caller's polls are what keep its generation fresh: there is no worker following it.
type GenerationGet struct {
	config         GenerationGetConfig
	dao            GenerationGetDao
	daoElectCheck  GenerationGetDaoElectCheck
	daoAttemptList GenerationGetDaoAttemptList
	serviceCheck   GenerationGetServiceCheck
}

// NewGenerationGet returns a new GenerationGet service.
func NewGenerationGet(
	config GenerationGetConfig,
	dao GenerationGetDao,
	daoElectCheck GenerationGetDaoElectCheck,
	daoAttemptList GenerationGetDaoAttemptList,
	serviceCheck GenerationGetServiceCheck,
) *GenerationGet {
	return &GenerationGet{
		config: config, dao: dao, daoElectCheck: daoElectCheck, daoAttemptList: daoAttemptList, serviceCheck: serviceCheck,
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

	generation, err := service.dao.Exec(ctx, &dao.GenerationGetRequest{
		ID: request.ID, OwnerID: request.OwnerID,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get generation: %w", err))
	}

	if generation.SettledAt == nil {
		generation, err = service.checkIfStale(ctx, generation)
		if err != nil {
			return nil, otel.ReportError(span, err)
		}
	}

	attempts, err := service.daoAttemptList.Exec(ctx, &dao.GenerationAttemptListRequest{GenerationID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("list attempts: %w", err))
	}

	return newGeneration(generation, attempts), nil
}

// checkIfStale checks the generation when no one checked it within the interval.
func (service *GenerationGet) checkIfStale(ctx context.Context, generation *dao.Generation) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationGet(checkIfStale)")
	defer span.End()

	elected, err := service.daoElectCheck.Exec(ctx, &dao.GenerationElectCheckRequest{
		ID:       generation.ID,
		OwnerID:  generation.OwnerID,
		Interval: service.config.CheckInterval,
	})

	// Checked within the interval, by this caller or another: the read is current enough.
	if errors.Is(err, dao.ErrGenerationCheckNotDue) {
		return generation, nil
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("elect check: %w", err))
	}

	checked, err := service.serviceCheck.Exec(ctx, &GenerationCheckRequest{Generation: elected})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("check generation: %w", err))
	}

	return checked, nil
}
