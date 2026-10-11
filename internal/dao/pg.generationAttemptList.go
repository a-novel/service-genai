package dao

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationAttemptList.sql
var generationAttemptListQuery string

// GenerationAttemptListRequest identifies the generation whose attempts to read.
type GenerationAttemptListRequest struct {
	GenerationID uuid.UUID
}

// PgGenerationAttemptList reads the provider's answer to each attempt of a generation, in order.
type PgGenerationAttemptList struct{}

// NewPgGenerationAttemptList returns a new PgGenerationAttemptList dao.
func NewPgGenerationAttemptList() *PgGenerationAttemptList {
	return &PgGenerationAttemptList{}
}

func (dao *PgGenerationAttemptList) Exec(
	ctx context.Context, request *GenerationAttemptListRequest,
) ([]*GenerationAttempt, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationAttemptList")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entities := make([]*GenerationAttempt, 0)

	err = tx.NewRaw(generationAttemptListQuery, request.GenerationID).Scan(ctx, &entities)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entities, nil
}
