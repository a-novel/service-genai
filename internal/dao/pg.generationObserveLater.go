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

//go:embed pg.generationObserveLater.sql
var generationObserveLaterQuery string

// GenerationObserveLaterRequest schedules another observation of a known provider operation.
type GenerationObserveLaterRequest struct {
	// ClaimToken must match the acquisition whose lease is still live.
	ClaimToken uuid.UUID
	ID         uuid.UUID
	WorkerID   string
	RetryAfter time.Duration
}

// GenerationObserveLater returns a generation to the queue while retaining its provider operation.
type GenerationObserveLater struct{}

func NewGenerationObserveLater() *GenerationObserveLater {
	return &GenerationObserveLater{}
}

func (dao *GenerationObserveLater) Exec(
	ctx context.Context,
	request *GenerationObserveLaterRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationObserveLater")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(
		generationObserveLaterQuery,
		request.ID,
		request.WorkerID,
		request.RetryAfter.Seconds(),
		request.ClaimToken,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationNotHeld)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
