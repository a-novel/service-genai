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

//go:embed pg.generationUsageSum.sql
var generationUsageSumQuery string

// GenerationUsageSumRequest bounds one sum.
type GenerationUsageSumRequest struct {
	OwnerID uuid.UUID
	// Purpose restricts the sum to one purpose; nil sums every purpose.
	Purpose *string
	// Window is how far back the sum reaches, from now, by the database clock.
	Window time.Duration
}

// GenerationUsageTotals is what an owner consumed over a window.
type GenerationUsageTotals struct {
	InputTokens  int64 `bun:"input_tokens"`
	OutputTokens int64 `bun:"output_tokens"`
}

// GenerationUsageSum adds up an owner's Usage Records over a rolling window.
type GenerationUsageSum struct{}

func NewGenerationUsageSum() *GenerationUsageSum {
	return &GenerationUsageSum{}
}

func (dao *GenerationUsageSum) Exec(
	ctx context.Context, request *GenerationUsageSumRequest,
) (*GenerationUsageTotals, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationUsageSum")
	defer span.End()

	span.SetAttributes(attribute.String("usage.window", request.Window.String()))

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	totals := &GenerationUsageTotals{}

	err = tx.NewRaw(
		generationUsageSumQuery, request.OwnerID, request.Purpose, request.Window.Seconds(),
	).Scan(ctx, totals)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return totals, nil
}
