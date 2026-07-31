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
)

// GenerationSubmitDao is the data-access dependency of [GenerationSubmit].
type GenerationSubmitDao interface {
	Exec(ctx context.Context, request *dao.GenerationSubmitRequest) (*dao.GenerationSubmitResult, error)
}

// GenerationSubmitRequest holds the parameters for a [GenerationSubmit.Exec] call.
type GenerationSubmitRequest struct {
	// OwnerID is the user the generation acts for, supplied by a caller that already verified it.
	OwnerID uuid.UUID `validate:"required"`
	// Purpose is what the caller attributes this spend to. Free-form: the vocabulary belongs to the
	// caller, and this service only groups by it.
	Purpose string `validate:"required,notblank,max=255"`
	// IdempotencyKey deduplicates repeat submissions within one owner. Required — an unkeyed
	// submission of a priced call is a bug, not a default.
	IdempotencyKey string `validate:"required,notblank,max=255"`
	// Request is the provider payload, forwarded verbatim. [RequestSizeCeiling] bounds its bytes.
	Request json.RawMessage `validate:"required"`
	// MaxAttempts caps the runs this generation gets. Zero means one, the right floor for a priced
	// call.
	MaxAttempts int16 `validate:"min=0,max=10"`
}

// GenerationSubmitResult reports the stored generation and how it got there.
type GenerationSubmitResult struct {
	Generation *dao.Generation
	// Created is false on a replay, so a retrying caller attaches to work already in flight rather
	// than paying for a second run.
	Created bool
}

// A GenerationSubmit records a generation for a worker to run.
type GenerationSubmit struct {
	dao GenerationSubmitDao
}

func NewGenerationSubmit(submitDao GenerationSubmitDao) *GenerationSubmit {
	return &GenerationSubmit{dao: submitDao}
}

func (service *GenerationSubmit) Exec(
	ctx context.Context, request *GenerationSubmitRequest,
) (*GenerationSubmitResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSubmit")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.owner_id", request.OwnerID.String()),
		attribute.String("generation.purpose", request.Purpose),
		attribute.Int("generation.request_bytes", len(request.Request)),
	)

	err := validateSubmit(request)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	maxAttempts := request.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 1
	}

	// The digest is what tells a replay from a reused key. It covers the request alone: the same
	// payload under the same key is the same work, whatever else the caller varied.
	fingerprint := sha256.Sum256(request.Request)

	result, err := service.dao.Exec(ctx, &dao.GenerationSubmitRequest{
		// Minted here so the created and replayed cases can be told apart without a second
		// round-trip. uuidv7 keeps the table's index locality under insert churn.
		ID:                 uuid.Must(uuid.NewV7()),
		OwnerID:            request.OwnerID,
		Purpose:            request.Purpose,
		IdempotencyKey:     request.IdempotencyKey,
		RequestFingerprint: fingerprint[:],
		Request:            request.Request,
		MaxAttempts:        maxAttempts,
	})

	if errors.Is(err, dao.ErrGenerationSubmitConflict) {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrIdempotencyConflict, err))
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("submit generation: %w", err))
	}

	span.SetAttributes(
		attribute.String("generation.id", result.Generation.ID.String()),
		attribute.Bool("generation.created", result.Created),
	)

	return otel.ReportSuccess(span, &GenerationSubmitResult{
		Generation: result.Generation,
		Created:    result.Created,
	}), nil
}

func validateSubmit(request *GenerationSubmitRequest) error {
	err := validate.Struct(request)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	if len(request.Request) > RequestSizeCeiling {
		return fmt.Errorf(
			"%w: request contains %d bytes, limit is %d",
			ErrInvalidRequest,
			len(request.Request),
			RequestSizeCeiling,
		)
	}

	// The payload is opaque, but it still has to be an object: the provider adapter merges its own
	// two fields into it, and there is nowhere to merge them into anything else. No tag expresses
	// this, so it stays an explicit check.
	var fields map[string]json.RawMessage

	err = json.Unmarshal(request.Request, &fields)
	if err != nil {
		return fmt.Errorf("%w: request is not a JSON object", ErrInvalidRequest)
	}

	return nil
}
