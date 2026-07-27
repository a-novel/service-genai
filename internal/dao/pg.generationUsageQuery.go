package dao

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationUsageQuery.sql
var generationUsageQueryQuery string

// GenerationUsageGroup is what one purpose and model consumed over the window.
type GenerationUsageGroup struct {
	Purpose string `bun:"purpose"`
	Model   string `bun:"model"`

	InputTokens       int64 `bun:"input_tokens"`
	CachedInputTokens int64 `bun:"cached_input_tokens"`
	OutputTokens      int64 `bun:"output_tokens"`
	ReasoningTokens   int64 `bun:"reasoning_tokens"`

	// Attempts counts the usage rows behind the group. A retried generation contributes more than
	// one, because it consumed more than once.
	Attempts int64 `bun:"attempts"`
}

// GenerationUsageQueryRequest is the input to [GenerationUsageQuery.Exec].
type GenerationUsageQueryRequest struct {
	OwnerID uuid.UUID
	// From is inclusive, To exclusive, so adjacent windows neither overlap nor skip a row.
	From time.Time
	To   time.Time
	// Purpose and Model narrow the result. Empty means no filter.
	Purpose string
	Model   string
}

// GenerationUsageQuery reads an owner's consumption over a window.
type GenerationUsageQuery struct{}

func NewGenerationUsageQuery() *GenerationUsageQuery {
	return new(GenerationUsageQuery)
}

func (dao *GenerationUsageQuery) Exec(
	ctx context.Context, request *GenerationUsageQueryRequest,
) ([]*GenerationUsageGroup, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationUsageQuery")
	defer span.End()

	span.SetAttributes(
		attribute.String("usage.owner_id", request.OwnerID.String()),
		attribute.String("usage.from", request.From.Format(time.RFC3339)),
		attribute.String("usage.to", request.To.Format(time.RFC3339)),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	var entities []*GenerationUsageGroup

	err = tx.NewRaw(
		generationUsageQueryQuery,
		request.OwnerID, request.From, request.To, request.Purpose, request.Model,
	).Scan(ctx, &entities)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	span.SetAttributes(attribute.Int("usage.groups", len(entities)))

	return otel.ReportSuccess(span, entities), nil
}
