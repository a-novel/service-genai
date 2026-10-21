package dao

import (
	"context"
	_ "embed"
	"fmt"

	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationQueueDepth.sql
var generationQueueDepthQuery string

// GenerationQueueDepth is the queue's backlog.
type GenerationQueueDepth struct {
	// Pending is the number of generations waiting for the provider to accept their call.
	Pending int64 `bun:"pending"`
	// OldestPendingAgeSeconds is how long the one waiting longest has waited. Zero when nothing pends.
	OldestPendingAgeSeconds float64 `bun:"oldest_pending_age_seconds"`
}

// GenerationQueueDepthRequest holds the parameters for a [PgGenerationQueueDepth.Exec] call.
type GenerationQueueDepthRequest struct{}

// PgGenerationQueueDepth reads the backlog for the health report.
type PgGenerationQueueDepth struct{}

// NewPgGenerationQueueDepth returns a new PgGenerationQueueDepth dao.
func NewPgGenerationQueueDepth() *PgGenerationQueueDepth {
	return &PgGenerationQueueDepth{}
}

func (dao *PgGenerationQueueDepth) Exec(
	ctx context.Context, _ *GenerationQueueDepthRequest,
) (*GenerationQueueDepth, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationQueueDepth")
	defer span.End()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	entity := &GenerationQueueDepth{}

	err = db.NewRaw(generationQueueDepthQuery).Scan(ctx, entity)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	span.SetAttributes(attribute.Int64("queue.pending", entity.Pending))

	return entity, nil
}
