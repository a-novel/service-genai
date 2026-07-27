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

//go:embed pg.generationRequeue.sql
var generationRequeueQuery string

// GenerationRequeueRequest is the input to [GenerationRequeue.Exec].
type GenerationRequeueRequest struct {
	ID       uuid.UUID
	WorkerID string
}

// GenerationRequeue returns a generation to the queue after a retryable failure.
//
// It is not a public operation on its own: a worker reports an outcome, and a retryable failure with
// attempts remaining lands here rather than in [GenerationSettle]. Folding the two together is what
// stops a worker handing back work it already reported failed.
type GenerationRequeue struct{}

func NewGenerationRequeue() *GenerationRequeue {
	return new(GenerationRequeue)
}

func (dao *GenerationRequeue) Exec(ctx context.Context, request *GenerationRequeueRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationRequeue")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.worker_id", request.WorkerID),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := new(Generation)

	err = tx.NewRaw(generationRequeueQuery, request.ID, request.WorkerID).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationNotHeld)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return otel.ReportSuccess(span, entity), nil
}
