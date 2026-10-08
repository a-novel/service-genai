package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
)

// Dependencies of [GenerationSubmit].
type (
	// GenerationSubmitDao records the generation.
	GenerationSubmitDao interface {
		Exec(ctx context.Context, request *dao.GenerationSubmitRequest) (*dao.GenerationSubmitResult, error)
	}
	// GenerationSubmitUsageListDao reads what a replayed generation consumed.
	GenerationSubmitUsageListDao interface {
		Exec(ctx context.Context, request *dao.GenerationUsageListRequest) ([]*dao.GenerationUsage, error)
	}
	// GenerationSubmitServiceCheck starts a newly recorded generation.
	GenerationSubmitServiceCheck interface {
		Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
	}
)

// GenerationSubmitConfig is what a [GenerationSubmit] needs to run.
type GenerationSubmitConfig struct {
	// MaxAttempts caps the provider calls a generation gets when a call fails retryably.
	MaxAttempts int16 `validate:"required,min=1,max=10"`
}

// GenerationSubmitRequest holds the parameters for a [GenerationSubmit.Exec] call.
type GenerationSubmitRequest struct {
	// OwnerID is the user the generation acts for, supplied by a caller that already verified it.
	OwnerID uuid.UUID `validate:"required"`
	// Purpose is what the caller attributes this spend to. Free-form: the vocabulary belongs to the
	// caller, and this service only groups by it.
	Purpose string `validate:"required,notblank,max=255"`
	// IdempotencyKey deduplicates repeat submissions within one owner.
	IdempotencyKey string `validate:"required,notblank,max=255"`
	// Tier is the level of model capability wanted.
	Tier lib.Tier `validate:"required,oneof=fast balanced deep"`
	// Instructions are the trusted channel.
	Instructions string `validate:"required,notblank"`
	// Input is the untrusted channel: any JSON value.
	Input json.RawMessage `validate:"required"`
	// OutputSchema is the JSON Schema the output conforms to: a JSON object.
	OutputSchema json.RawMessage `validate:"required"`
}

// GenerationSubmitResult reports the stored generation and how it got there.
type GenerationSubmitResult struct {
	Generation *Generation
	// Created is false on a replay, so a retrying caller attaches to work already in flight rather
	// than paying for a second run.
	Created bool
}

// A GenerationSubmit records a generation and starts its provider call.
//
// A replay returns the recorded generation as it stands, without checking it: the caller polls
// that generation next, and the poll checks it.
type GenerationSubmit struct {
	config   GenerationSubmitConfig
	dao      GenerationSubmitDao
	usageDao GenerationSubmitUsageListDao
	check    GenerationSubmitServiceCheck
}

func NewGenerationSubmit(
	config GenerationSubmitConfig,
	submitDao GenerationSubmitDao,
	usageDao GenerationSubmitUsageListDao,
	check GenerationSubmitServiceCheck,
) (*GenerationSubmit, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	return &GenerationSubmit{config: config, dao: submitDao, usageDao: usageDao, check: check}, nil
}

func (service *GenerationSubmit) Exec(
	ctx context.Context, request *GenerationSubmitRequest,
) (*GenerationSubmitResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSubmit")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.owner_id", request.OwnerID.String()),
		attribute.String("generation.purpose", request.Purpose),
		attribute.String("generation.tier", string(request.Tier)),
		attribute.Int("generation.request_bytes", requestBytes(request)),
	)

	err := validateSubmit(request)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	stored, err := json.Marshal(&generationRequest{
		Tier:         request.Tier,
		Instructions: request.Instructions,
		Input:        request.Input,
		OutputSchema: request.OutputSchema,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("encode request: %w", err))
	}

	// The digest is what tells a replay from a reused key.
	fingerprint := sha256.Sum256(stored)

	result, err := service.dao.Exec(ctx, &dao.GenerationSubmitRequest{
		// Minted here so the created and replayed cases can be told apart without a second
		// round-trip. uuidv7 keeps the table's index locality under insert churn.
		ID:                 uuid.Must(uuid.NewV7()),
		OwnerID:            request.OwnerID,
		Purpose:            request.Purpose,
		IdempotencyKey:     request.IdempotencyKey,
		RequestFingerprint: fingerprint[:],
		Request:            stored,
		MaxAttempts:        service.config.MaxAttempts,
	})

	if errors.Is(err, dao.ErrGenerationSubmitConflict) {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrIdempotencyConflict, err))
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("submit generation: %w", err))
	}

	span.SetAttributes(attribute.String("generation.id", result.Generation.ID.String()))

	generation := result.Generation

	if result.Created {
		generation, err = service.check.Exec(ctx, &GenerationCheckRequest{Generation: generation})
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("start generation: %w", err))
		}
	}

	usage, err := service.usageDao.Exec(ctx, &dao.GenerationUsageListRequest{GenerationID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("list usage: %w", err))
	}

	return &GenerationSubmitResult{
		Generation: newGeneration(generation, usage),
		Created:    result.Created,
	}, nil
}

func requestBytes(request *GenerationSubmitRequest) int {
	return len(request.Instructions) + len(request.Input) + len(request.OutputSchema)
}

func validateSubmit(request *GenerationSubmitRequest) error {
	err := validate.Struct(request)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	if size := requestBytes(request); size > RequestSizeCeiling {
		return fmt.Errorf(
			"%w: request contains %d bytes, limit is %d", ErrInvalidRequest, size, RequestSizeCeiling,
		)
	}

	if !json.Valid(request.Input) {
		return fmt.Errorf("%w: input is not JSON", ErrInvalidRequest)
	}

	// The schema's content is the provider's to judge; its shape is not. A strict schema's root is
	// always an object.
	var schema map[string]json.RawMessage

	err = json.Unmarshal(request.OutputSchema, &schema)
	if err != nil {
		return fmt.Errorf("%w: output schema is not a JSON object", ErrInvalidRequest)
	}

	return nil
}
