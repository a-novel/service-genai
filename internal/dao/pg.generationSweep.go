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

//go:embed pg.generationSweep.sql
var generationSweepQuery string

// GenerationSweepRequest bounds one sweep.
type GenerationSweepRequest struct {
	// Interval is how long a check stays fresh.
	Interval time.Duration
	// Limit caps the batch.
	Limit int
}

// GenerationSweep takes the unsettled generations nobody checked within the interval, marking them
// checked.
type GenerationSweep struct{}

func NewGenerationSweep() *GenerationSweep {
	return &GenerationSweep{}
}

func (dao *GenerationSweep) Exec(ctx context.Context, request *GenerationSweepRequest) ([]*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationSweep")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entities := make([]*Generation, 0)

	err = tx.NewRaw(generationSweepQuery, request.Interval.Seconds(), request.Limit).Scan(ctx, &entities)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	span.SetAttributes(attribute.Int("sweep.generations", len(entities)))

	return entities, nil
}
