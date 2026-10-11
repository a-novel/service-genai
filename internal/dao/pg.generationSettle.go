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

//go:embed pg.generationSettle.sql
var generationSettleQuery string

// GenerationSettleRequest is the input to [GenerationSettle.Exec].
type GenerationSettleRequest struct {
	ID uuid.UUID
	// Attempt, StartRequestedAt and ProviderCallID are what the caller observed. A generation that
	// has since moved on is not settled.
	Attempt          int16
	StartRequestedAt *time.Time
	ProviderCallID   *string
	// Status is the terminal state to land in. The terminal-fields constraint rejects any other.
	Status GenerationStatus
	// Error names the cause of a failure or cancellation. Empty on success.
	Error *string
	// Retention is how long the settled row survives before the purge takes it.
	Retention time.Duration
}

// GenerationSettle records a terminal outcome.
//
// It does not record the attempt's answer. A transition without it loses what the attempt consumed,
// so the caller wraps both in one transaction. See [GenerationAttemptInsert].
type GenerationSettle struct{}

func NewGenerationSettle() *GenerationSettle {
	return &GenerationSettle{}
}

func (dao *GenerationSettle) Exec(ctx context.Context, request *GenerationSettleRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationSettle")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(
		generationSettleQuery,
		request.ID,
		request.Attempt,
		request.ProviderCallID,
		string(request.Status),
		request.Error,
		request.Retention.Seconds(),
		request.StartRequestedAt,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationChanged)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
