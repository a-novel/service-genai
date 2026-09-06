package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationBeginStart.sql
var generationBeginStartQuery string

// GenerationBeginStartRequest identifies the live claim to control.
type GenerationBeginStartRequest struct {
	// ClaimToken must match the acquisition whose lease is still live.
	ClaimToken uuid.UUID
	ID         uuid.UUID
	WorkerID   string
}

// GenerationBeginStart records Start intent unless cancellation has already committed.
type GenerationBeginStart struct{}

func NewGenerationBeginStart() *GenerationBeginStart {
	return &GenerationBeginStart{}
}

func (dao *GenerationBeginStart) Exec(
	ctx context.Context,
	request *GenerationBeginStartRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationBeginStart")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.worker_id", request.WorkerID),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(
		generationBeginStartQuery,
		request.ID,
		request.WorkerID,
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
