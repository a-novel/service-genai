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

// GenerationSettleRequest is the input to [PgGenerationSettle.Exec].
type GenerationSettleRequest struct {
	ID uuid.UUID
	// Attempt is the attempt the caller observed. The settle applies only while the generation still
	// carries it.
	Attempt int16
	// StartRequestedAt is the start the caller observed. A start released and taken again keeps the
	// attempt and the empty call, so only the start tells the two apart.
	StartRequestedAt *time.Time
	// ProviderCallID is the call the caller observed, nil when it observed none.
	ProviderCallID *string
	// Status is the terminal state to land in. The terminal-fields constraint rejects any other.
	Status GenerationStatus
	// Error names the cause of a failure or cancellation. Empty on success.
	Error *string
	// Retention is how long the settled row survives before the purge takes it.
	Retention time.Duration
	// OutcomeUnknown marks a failure after which the provider may still run the call: a start it may
	// have accepted, or a stop that never took.
	OutcomeUnknown bool
}

// PgGenerationSettle records a terminal outcome.
//
// It does not record the attempt's answer. A transition without it loses what the attempt consumed,
// so the caller wraps both in one transaction. See [PgGenerationAttemptInsert].
type PgGenerationSettle struct{}

// NewPgGenerationSettle returns a new PgGenerationSettle dao.
func NewPgGenerationSettle() *PgGenerationSettle {
	return &PgGenerationSettle{}
}

func (dao *PgGenerationSettle) Exec(ctx context.Context, request *GenerationSettleRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationSettle")
	defer span.End()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	entity := &Generation{}

	err = db.NewRaw(
		generationSettleQuery,
		request.ID,
		request.Attempt,
		request.ProviderCallID,
		string(request.Status),
		request.Error,
		request.Retention.Seconds(),
		request.StartRequestedAt,
		request.OutcomeUnknown,
	).Scan(ctx, entity)
	if errors.Is(err, sql.ErrNoRows) {
		err = errors.Join(err, ErrGenerationChanged)
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", refused(err)))
	}

	return entity, nil
}
