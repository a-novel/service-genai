package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationControl.sql
var generationControlQuery string

// GenerationControlRequest identifies the live claim to control.
type GenerationControlRequest struct {
	// ClaimToken must match the acquisition whose lease is still live.
	ClaimToken uuid.UUID
	ID         uuid.UUID
	WorkerID   string
	Lease      time.Duration
}

// GenerationControl renews a live claim and returns current cancellation state.
type GenerationControl struct{}

func NewGenerationControl() *GenerationControl {
	return &GenerationControl{}
}

func (dao *GenerationControl) Exec(
	ctx context.Context,
	request *GenerationControlRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationControl")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.worker_id", request.WorkerID),
		attribute.Float64("claim.lease_seconds", request.Lease.Seconds()),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(
		generationControlQuery,
		request.ID,
		request.WorkerID,
		request.Lease.Seconds(),
		request.ClaimToken,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationNotHeld)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return otel.ReportSuccess(span, entity), nil
}
