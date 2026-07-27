package dao

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationReap.sql
var generationReapQuery string

// GenerationReapRequest is the input to [GenerationReap.Exec].
type GenerationReapRequest struct {
	// Grace is how long past its lease a claim is left alone, so a late settle beats the sweep.
	Grace time.Duration
	// Retention applies to the generations this sweep abandons, which are terminal.
	Retention time.Duration
	// Limit caps one sweep. Bounded by the caller, which repeats until a sweep comes back short.
	Limit int
}

// GenerationReap recovers generations whose worker died mid-run.
type GenerationReap struct{}

func NewGenerationReap() *GenerationReap {
	return new(GenerationReap)
}

func (dao *GenerationReap) Exec(ctx context.Context, request *GenerationReapRequest) ([]*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationReap")
	defer span.End()

	span.SetAttributes(
		attribute.Float64("reap.grace_seconds", request.Grace.Seconds()),
		attribute.Int("reap.limit", request.Limit),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	var entities []*Generation

	err = tx.NewRaw(
		generationReapQuery, request.Grace.Seconds(), request.Retention.Seconds(), request.Limit,
	).Scan(ctx, &entities)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	span.SetAttributes(attribute.Int("reap.recovered", len(entities)))

	return otel.ReportSuccess(span, entities), nil
}
