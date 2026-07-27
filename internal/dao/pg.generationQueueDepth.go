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
	// Pending is the number of generations due to run that no worker has claimed.
	Pending int64 `bun:"pending"`
	// OldestPendingAgeSeconds is how long the oldest of them has waited. Zero when nothing pends.
	OldestPendingAgeSeconds float64 `bun:"oldest_pending_age_seconds"`
}

// GenerationQueueDepthDao reads the backlog for the health report.
type GenerationQueueDepthDao struct{}

func NewGenerationQueueDepth() *GenerationQueueDepthDao {
	return new(GenerationQueueDepthDao)
}

func (dao *GenerationQueueDepthDao) Exec(ctx context.Context) (*GenerationQueueDepth, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationQueueDepth")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := new(GenerationQueueDepth)

	err = tx.NewRaw(generationQueueDepthQuery).Scan(ctx, entity)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	span.SetAttributes(attribute.Int64("queue.pending", entity.Pending))

	return otel.ReportSuccess(span, entity), nil
}
