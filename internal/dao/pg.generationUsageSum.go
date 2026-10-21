package dao

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/uptrace/bun"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationUsageSum.sql
var generationUsageSumQuery string

// GenerationUsageSumRequest bounds the sums of one cap.
type GenerationUsageSumRequest struct {
	OwnerID uuid.UUID
	// Purpose restricts the sums to one purpose; nil sums every purpose.
	Purpose *string
	// Windows are how far back each sum reaches, from now, by the database clock. At least one.
	Windows []time.Duration
}

// GenerationUsageTotals is what an owner consumed over a window.
type GenerationUsageTotals struct {
	InputTokens  int64 `bun:"input_tokens"`
	OutputTokens int64 `bun:"output_tokens"`
}

// PgGenerationUsageSum adds up an owner's Usage Records over rolling windows, in one query. It
// returns one total per window, in the order of the windows.
type PgGenerationUsageSum struct{}

// NewPgGenerationUsageSum returns a new PgGenerationUsageSum dao.
func NewPgGenerationUsageSum() *PgGenerationUsageSum {
	return &PgGenerationUsageSum{}
}

func (dao *PgGenerationUsageSum) Exec(
	ctx context.Context, request *GenerationUsageSumRequest,
) ([]*GenerationUsageTotals, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationUsageSum")
	defer span.End()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	windows := lo.Map(request.Windows, func(window time.Duration, _ int) float64 { return window.Seconds() })
	totals := make([]*GenerationUsageTotals, 0, len(windows))

	err = db.NewRaw(
		generationUsageSumQuery, request.OwnerID, request.Purpose, bun.List(windows), len(windows),
	).Scan(ctx, &totals)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return totals, nil
}
