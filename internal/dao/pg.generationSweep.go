package dao

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationSweep.sql
var generationSweepQuery string

// GenerationSweepRequest names one page of a sweep.
type GenerationSweepRequest struct {
	// After is the last generation of the previous page. The zero UUID starts the first page.
	After uuid.UUID
	// Limit caps the page.
	Limit int
}

// PgGenerationSweep reads one page of the unsettled generations, in id order.
type PgGenerationSweep struct{}

// NewPgGenerationSweep returns a new PgGenerationSweep dao.
func NewPgGenerationSweep() *PgGenerationSweep {
	return &PgGenerationSweep{}
}

func (dao *PgGenerationSweep) Exec(ctx context.Context, request *GenerationSweepRequest) ([]*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationSweep")
	defer span.End()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	entities := make([]*Generation, 0)

	err = db.NewRaw(
		generationSweepQuery, request.After, request.Limit,
	).Scan(ctx, &entities)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	span.SetAttributes(attribute.Int("sweep.generations", len(entities)))

	return entities, nil
}
