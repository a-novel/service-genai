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

// ErrGenerationUsageExists is returned when this attempt's usage is already recorded. The stop of an
// orphaned call records it outside any settle, and a call id write that landed late lets a check
// settle the same call afterwards.
var ErrGenerationUsageExists = errors.New("usage already recorded for this attempt")

// GenerationUsageInsertRequest is the input to [PgGenerationUsageInsert.Exec].
type GenerationUsageInsertRequest struct {
	GenerationID uuid.UUID
	Attempt      int16
	OwnerID      uuid.UUID
	Purpose      string
	Provider     string
	// Model is the model snapshot the provider's response reports.
	Model string
	// Class is the reasoning effort the provider's response reports.
	Class string
	// Tier is the service tier the provider's response reports, nil when it reports none.
	Tier *string

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
// usage is a billed call nothing counts. The stop of an orphaned call, which no transition tracks,
// calls it alone.
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

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	entity := &GenerationUsage{}

	err = db.NewRaw(
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
	if errors.Is(err, sql.ErrNoRows) {
		err = errors.Join(err, ErrGenerationUsageExists)
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", refused(err)))
	}

	return entity, nil
}
