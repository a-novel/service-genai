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

// Dependencies of [GenerationGet].
type (
	// GenerationGetDao reads the generation.
	GenerationGetDao interface {
		Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
	}
	// GenerationGetElectCheckDao elects this read to check a stale generation.
	GenerationGetElectCheckDao interface {
		Exec(ctx context.Context, request *dao.GenerationElectCheckRequest) (*dao.Generation, error)
	}
	// GenerationGetUsageListDao reads what the generation consumed.
	GenerationGetUsageListDao interface {
		Exec(ctx context.Context, request *dao.GenerationUsageListRequest) ([]*dao.GenerationUsage, error)
	}
	// GenerationGetServiceCheck checks the generation.
	GenerationGetServiceCheck interface {
		Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
	}
)

// GenerationGetConfig is what a [GenerationGet] needs to run.
type GenerationGetConfig struct {
	// CheckInterval is how long a check stays fresh. However often callers poll, a generation reaches
	// the provider at most once per interval.
	CheckInterval time.Duration `validate:"required,gt=0"`
}

// GenerationGetRequest holds the parameters for a [GenerationGet.Exec] call.
type GenerationGetRequest struct {
	ID uuid.UUID `validate:"required"`
	// OwnerID scopes the read, and is not optional: it is the whole ownership predicate.
	OwnerID uuid.UUID `validate:"required"`
}

// A GenerationGet reads one of an owner's generations, checking it first when it is stale.
//
// A caller's polls are what keep its generation fresh: there is no worker following it.
type GenerationGet struct {
	config   GenerationGetConfig
	dao      GenerationGetDao
	electDao GenerationGetElectCheckDao
	usageDao GenerationGetUsageListDao
	check    GenerationGetServiceCheck
}

func NewGenerationGet(
	config GenerationGetConfig,
	getDao GenerationGetDao,
	electDao GenerationGetElectCheckDao,
	usageDao GenerationGetUsageListDao,
	check GenerationGetServiceCheck,
) (*GenerationGet, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	return &GenerationGet{config: config, dao: getDao, electDao: electDao, usageDao: usageDao, check: check}, nil
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

	if generation.SettledAt == nil {
		generation, err = service.checkIfStale(ctx, generation)
		if err != nil {
			return nil, otel.ReportError(span, err)
		}
	}

	usage, err := service.usageDao.Exec(ctx, &dao.GenerationUsageListRequest{GenerationID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("list usage: %w", err))
	}

	return newGeneration(generation, usage), nil
}

// checkIfStale checks the generation when no one checked it within the interval.
func (service *GenerationGet) checkIfStale(ctx context.Context, generation *dao.Generation) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationGet(checkIfStale)")
	defer span.End()

	elected, err := service.electDao.Exec(ctx, &dao.GenerationElectCheckRequest{
		ID: generation.ID, OwnerID: generation.OwnerID, Interval: service.config.CheckInterval,
	})

	// Checked within the interval, by this caller or another: the read is current enough.
	if errors.Is(err, dao.ErrGenerationCheckNotDue) {
		return generation, nil
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("elect check: %w", err))
	}

	checked, err := service.check.Exec(ctx, &GenerationCheckRequest{Generation: elected})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("check generation: %w", err))
	}

	return checked, nil
}
