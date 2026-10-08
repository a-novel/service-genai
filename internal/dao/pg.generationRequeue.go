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

//go:embed pg.generationRequeue.sql
var generationRequeueQuery string

// GenerationRequeueRequest identifies the failed attempt to retry.
type GenerationRequeueRequest struct {
	ID      uuid.UUID
	Attempt int16
	// ProviderCallID is the finished operation of the failed attempt.
	ProviderCallID string
	// Delay postpones the next start.
	Delay time.Duration
}

// GenerationRequeue returns a generation whose attempt failed retryably to the queue for a fresh
// provider call.
//
// It does not write the attempt's usage row; the caller wraps both in one transaction.
type GenerationRequeue struct{}

func NewGenerationRequeue() *GenerationRequeue {
	return &GenerationRequeue{}
}

func (dao *GenerationRequeue) Exec(ctx context.Context, request *GenerationRequeueRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationRequeue")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(
		generationRequeueQuery, request.ID, request.Attempt, request.ProviderCallID, request.Delay.Seconds(),
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationChanged)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
