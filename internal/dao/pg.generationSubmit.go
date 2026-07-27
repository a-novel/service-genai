package dao

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationSubmit.sql
var generationSubmitQuery string

// ErrGenerationSubmitConflict is returned by [GenerationSubmit.Exec] when the idempotency key is
// already held by a generation submitted with a different request.
//
// This is the case a replay must not be confused with. A replay repeats a request the caller may
// never have seen the answer to, and attaching it to the work already in flight is what stops the
// platform paying twice. A key reused with different content is instead a caller bug, and silently
// returning the earlier generation would answer a question that was never asked.
var ErrGenerationSubmitConflict = errors.New("idempotency key already used with a different request")

// GenerationSubmitRequest is the input to [GenerationSubmit.Exec].
type GenerationSubmitRequest struct {
	// ID the generation takes if this submission creates it. Minted by the caller so the created
	// and replayed cases can be told apart without a second round-trip.
	ID uuid.UUID
	// OwnerID is the user the generation acts for, and the attribution key for its cost.
	OwnerID uuid.UUID
	// Purpose names why the platform is spending on this call.
	Purpose string
	// Profile names the abstract execution tier to resolve.
	Profile string
	// IdempotencyKey deduplicates repeat submissions within one owner and purpose.
	IdempotencyKey string
	// RequestFingerprint is the digest of Request, used to tell a replay from a reused key.
	RequestFingerprint []byte
	// Request is the provider-agnostic generation request.
	Request json.RawMessage
	// MaxAttempts caps the runs this generation gets.
	MaxAttempts int16
}

// GenerationSubmitResult reports the stored generation and how it got there.
type GenerationSubmitResult struct {
	// Generation is the stored row: the newly created one, or the existing one on a replay.
	Generation *Generation
	// Created is false when an existing generation was returned under the same idempotency key, so
	// a caller retrying a request attaches to work already in flight rather than paying for a
	// second one.
	Created bool
}

// GenerationSubmit records a generation, deduplicating on the idempotency key.
type GenerationSubmit struct{}

func NewGenerationSubmit() *GenerationSubmit {
	return new(GenerationSubmit)
}

func (dao *GenerationSubmit) Exec(
	ctx context.Context, request *GenerationSubmitRequest,
) (*GenerationSubmitResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationSubmit")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.owner_id", request.OwnerID.String()),
		attribute.String("generation.purpose", request.Purpose),
		attribute.String("generation.profile", request.Profile),
		attribute.Int("generation.max_attempts", int(request.MaxAttempts)),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := new(Generation)

	err = tx.NewRaw(
		generationSubmitQuery,
		request.ID,
		request.OwnerID,
		request.Purpose,
		request.Profile,
		request.IdempotencyKey,
		request.RequestFingerprint,
		request.Request,
		request.MaxAttempts,
	).Scan(ctx, entity)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	// The insert either stored the identifier it was given, or lost to a row that already held the
	// key and returned that one instead. Comparing the two is what distinguishes the cases: bun is
	// configured to discard unknown columns, so a RETURNING expression such as (xmax = 0) is
	// silently dropped before it can be read and cannot be used for this.
	created := entity.ID == request.ID

	if !created && !bytes.Equal(entity.RequestFingerprint, request.RequestFingerprint) {
		return nil, otel.ReportError(span, ErrGenerationSubmitConflict)
	}

	span.SetAttributes(attribute.Bool("generation.created", created))

	return otel.ReportSuccess(span, &GenerationSubmitResult{Generation: entity, Created: created}), nil
}
