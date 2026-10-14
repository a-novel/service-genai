package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationUsageInsert.sql
var generationUsageInsertQuery string

// ErrGenerationUsageExists is returned when this attempt's usage is already recorded. A settle that
// lands twice must not count it twice.
var ErrGenerationUsageExists = errors.New("usage already recorded for this attempt")

// GenerationUsageInsertRequest is the input to [PgGenerationUsageInsert.Exec].
type GenerationUsageInsertRequest struct {
	GenerationID uuid.UUID
	Attempt      int16
	OwnerID      uuid.UUID
	Purpose      string
	Provider     string
	// Model, Class and Tier as the provider's response reports them; Tier is nil when it reports none.
	Model string
	Class string
	Tier  *string

	InputTokens       int64
	CachedInputTokens int64
	CacheWriteTokens  int64
	OutputTokens      int64
	ReasoningTokens   int64

	StartedAt   time.Time
	CompletedAt *time.Time
}

// PgGenerationUsageInsert records what one answered attempt consumed.
//
// Call it in the same transaction as the settle or requeue it belongs to: a transition without its
// usage is a billed call nothing counts.
type PgGenerationUsageInsert struct{}

// NewPgGenerationUsageInsert returns a new PgGenerationUsageInsert dao.
func NewPgGenerationUsageInsert() *PgGenerationUsageInsert {
	return &PgGenerationUsageInsert{}
}

func (dao *PgGenerationUsageInsert) Exec(
	ctx context.Context, request *GenerationUsageInsertRequest,
) (*GenerationUsage, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationUsageInsert")
	defer span.End()

	span.SetAttributes(
		attribute.Int("generation.attempt", int(request.Attempt)),
		attribute.Int64("usage.input_tokens", request.InputTokens),
		attribute.Int64("usage.output_tokens", request.OutputTokens),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &GenerationUsage{}

	err = tx.NewRaw(
		generationUsageInsertQuery,
		request.GenerationID,
		request.Attempt,
		request.OwnerID,
		request.Purpose,
		request.Provider,
		request.Model,
		request.Class,
		request.Tier,
		request.InputTokens,
		request.CachedInputTokens,
		request.CacheWriteTokens,
		request.OutputTokens,
		request.ReasoningTokens,
		request.StartedAt,
		request.CompletedAt,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationUsageExists)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", refused(err)))
	}

	return entity, nil
}
