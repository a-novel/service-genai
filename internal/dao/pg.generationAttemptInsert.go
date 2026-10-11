package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationAttemptInsert.sql
var generationAttemptInsertQuery string

// ErrGenerationAttemptExists is returned when this attempt already has its answer recorded. A settle
// that lands twice must not record it twice.
var ErrGenerationAttemptExists = errors.New("attempt already recorded")

// GenerationAttemptInsertRequest is the input to [GenerationAttemptInsert.Exec].
type GenerationAttemptInsertRequest struct {
	GenerationID uuid.UUID
	Attempt      int16
	// Response is the provider's final answer, as sent.
	Response json.RawMessage
}

// GenerationAttemptInsert records the provider's answer to one attempt.
//
// Call it in the same transaction as the settle or requeue it belongs to: a transition without its
// answer loses what the attempt consumed.
type GenerationAttemptInsert struct{}

func NewGenerationAttemptInsert() *GenerationAttemptInsert {
	return &GenerationAttemptInsert{}
}

func (dao *GenerationAttemptInsert) Exec(
	ctx context.Context, request *GenerationAttemptInsertRequest,
) (*GenerationAttempt, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationAttemptInsert")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.GenerationID.String()),
		attribute.Int("generation.attempt", int(request.Attempt)),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &GenerationAttempt{}

	err = tx.NewRaw(
		generationAttemptInsertQuery,
		request.GenerationID,
		request.Attempt,
		request.Response,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationAttemptExists)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
