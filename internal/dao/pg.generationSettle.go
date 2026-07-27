package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationSettle.sql
var generationSettleQuery string

// ErrGenerationNotHeld is returned when the generation is not running under the given worker. It
// covers a lost race with the reaper as well as a plain wrong id, and both mean the same thing to a
// worker: stop, the claim is gone.
var ErrGenerationNotHeld = errors.New("generation is not held by this worker")

// GenerationSettleRequest is the input to [GenerationSettle.Exec]. Exactly one of Output and Error
// is set: an output on success, an error otherwise.
type GenerationSettleRequest struct {
	ID       uuid.UUID
	WorkerID string
	// Status is the terminal state to land in. The caller supplies a terminal one; the
	// terminal-fields constraint rejects anything else.
	Status GenerationStatus
	// Output is the provider's structured output on success.
	Output json.RawMessage
	// Error is the serialised failure otherwise.
	Error *string
	// Retention is how long the settled row survives before the purge takes it. Only the row's user
	// content goes; its usage rows are kept.
	Retention time.Duration
}

// GenerationSettle records a terminal outcome.
//
// It does not write the usage row. That is a second statement, and the two have to land together —
// a transition without its usage row is an untracked charge — so the caller wraps both in one
// transaction. See [GenerationUsageInsert].
type GenerationSettle struct{}

func NewGenerationSettle() *GenerationSettle {
	return new(GenerationSettle)
}

func (dao *GenerationSettle) Exec(ctx context.Context, request *GenerationSettleRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationSettle")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.worker_id", request.WorkerID),
		attribute.String("generation.status", string(request.Status)),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := new(Generation)

	err = tx.NewRaw(
		generationSettleQuery,
		request.ID,
		request.WorkerID,
		string(request.Status),
		request.Output,
		request.Error,
		request.Retention.Seconds(),
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationNotHeld)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return otel.ReportSuccess(span, entity), nil
}
