package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationUsageInsert.sql
var generationUsageInsertQuery string

// ErrGenerationUsageExists is returned when this attempt already has a usage row. A settle that
// lands twice must not double-count.
var ErrGenerationUsageExists = errors.New("usage already recorded for this attempt")

// GenerationUsageInsertRequest is the input to [GenerationUsageInsert.Exec].
type GenerationUsageInsertRequest struct {
	GenerationID uuid.UUID
	Attempt      int16
	OwnerID      uuid.UUID
	Purpose      string
	// Provider and Model as the provider reported them, not as the request asked.
	Provider string
	Model    string

	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ReasoningTokens   int64
}

// GenerationUsageInsert records what one attempt consumed.
//
// Call it in the same transaction as the settle it belongs to: a terminal transition without its
// usage row is an untracked charge.
type GenerationUsageInsert struct{}

func NewGenerationUsageInsert() *GenerationUsageInsert {
	return new(GenerationUsageInsert)
}

func (dao *GenerationUsageInsert) Exec(
	ctx context.Context, request *GenerationUsageInsertRequest,
) (*GenerationUsage, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationUsageInsert")
	defer span.End()

	span.SetAttributes(
		attribute.String("usage.generation_id", request.GenerationID.String()),
		attribute.Int("usage.attempt", int(request.Attempt)),
		attribute.String("usage.provider", request.Provider),
		attribute.String("usage.model", request.Model),
		attribute.Int64("usage.input_tokens", request.InputTokens),
		attribute.Int64("usage.output_tokens", request.OutputTokens),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := new(GenerationUsage)

	err = tx.NewRaw(
		generationUsageInsertQuery,
		request.GenerationID,
		request.Attempt,
		request.OwnerID,
		request.Purpose,
		request.Provider,
		request.Model,
		request.InputTokens,
		request.CachedInputTokens,
		request.OutputTokens,
		request.ReasoningTokens,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationUsageExists)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return otel.ReportSuccess(span, entity), nil
}
