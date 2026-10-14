package dao

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationAttemptInsert.sql
var generationAttemptInsertQuery string

// GenerationAttemptInsertRequest is the input to [PgGenerationAttemptInsert.Exec].
type GenerationAttemptInsertRequest struct {
	GenerationID uuid.UUID
	Attempt      int16
	// Response is the provider's final answer, as sent.
	Response json.RawMessage
}

// PgGenerationAttemptInsert records the provider's answer to one attempt.
//
// Call it in the same transaction as the settle or requeue it belongs to: a transition without its
// answer loses what the attempt consumed.
type PgGenerationAttemptInsert struct{}

// NewPgGenerationAttemptInsert returns a new PgGenerationAttemptInsert dao.
func NewPgGenerationAttemptInsert() *PgGenerationAttemptInsert {
	return &PgGenerationAttemptInsert{}
}

func (dao *PgGenerationAttemptInsert) Exec(
	ctx context.Context, request *GenerationAttemptInsertRequest,
) (*GenerationAttempt, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationAttemptInsert")
	defer span.End()

	span.SetAttributes(
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
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", refused(err)))
	}

	return entity, nil
}
