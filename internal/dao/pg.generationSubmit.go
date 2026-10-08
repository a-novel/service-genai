package dao

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationSubmit.sql
var generationSubmitQuery string

// GenerationSubmitRequest is the input to [GenerationSubmit.Exec].
type GenerationSubmitRequest struct {
	// ID the generation takes if this submission creates it. Minted by the caller, so created and
	// replayed can be told apart without a second round-trip.
	ID uuid.UUID
	// OwnerID is the user the generation acts for, and the attribution key for its cost.
	OwnerID uuid.UUID
	// Purpose is what the caller attributes this spend to. Its vocabulary belongs to the caller.
	Purpose string
	// RequestKey identifies the request: another submission with the same key is a replay while its
	// generation is pending, running or succeeded.
	RequestKey  []byte
	Request     json.RawMessage
	MaxAttempts int16
}

// GenerationSubmitResult reports the stored generation and how it got there.
type GenerationSubmitResult struct {
	// Generation is the newly created row, or the existing one on a replay.
	Generation *Generation
	// Created is false on a replay, so a retrying caller attaches to work already in flight rather
	// than paying for a second run.
	Created bool
}

// GenerationSubmit records a generation, deduplicating on the request key.
type GenerationSubmit struct{}

func NewGenerationSubmit() *GenerationSubmit {
	return &GenerationSubmit{}
}

func (dao *GenerationSubmit) Exec(
	ctx context.Context, request *GenerationSubmitRequest,
) (*GenerationSubmitResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationSubmit")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
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
		request.RequestKey,
		request.Request,
		request.MaxAttempts,
	).Scan(ctx, entity)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	// Comparing identifiers is what distinguishes the cases: bun discards unknown columns, so a
	// RETURNING expression such as (xmax = 0) is dropped before it can be read.
	created := entity.ID == request.ID

	span.SetAttributes(attribute.Bool("generation.created", created))

	return &GenerationSubmitResult{Generation: entity, Created: created}, nil
}
