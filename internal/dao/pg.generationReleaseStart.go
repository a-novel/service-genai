package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationReleaseStart.sql
var generationReleaseStartQuery string

// GenerationReleaseStartRequest identifies the start intent to withdraw.
type GenerationReleaseStartRequest struct {
	ID uuid.UUID
	// Attempt is the attempt whose intent is withdrawn.
	Attempt int16
	// Delay postpones the next start.
	Delay time.Duration
}

// GenerationReleaseStart withdraws a start intent whose call the provider never accepted, giving
// the attempt back.
type GenerationReleaseStart struct{}

func NewGenerationReleaseStart() *GenerationReleaseStart {
	return &GenerationReleaseStart{}
}

func (dao *GenerationReleaseStart) Exec(
	ctx context.Context, request *GenerationReleaseStartRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationReleaseStart")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(generationReleaseStartQuery, request.ID, request.Attempt, request.Delay.Seconds()).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationChanged)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
