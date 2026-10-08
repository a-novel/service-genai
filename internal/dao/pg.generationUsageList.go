package dao

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationUsageList.sql
var generationUsageListQuery string

// GenerationUsageListRequest identifies the generation whose usage to read.
type GenerationUsageListRequest struct {
	GenerationID uuid.UUID
}

// GenerationUsageList reads what each provider call of a generation consumed, in attempt order.
type GenerationUsageList struct{}

func NewGenerationUsageList() *GenerationUsageList {
	return &GenerationUsageList{}
}

func (dao *GenerationUsageList) Exec(
	ctx context.Context, request *GenerationUsageListRequest,
) ([]*GenerationUsage, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationUsageList")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entities := make([]*GenerationUsage, 0)

	err = tx.NewRaw(generationUsageListQuery, request.GenerationID).Scan(ctx, &entities)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entities, nil
}
