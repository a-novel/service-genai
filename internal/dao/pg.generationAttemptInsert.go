package dao

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationAttemptInsert.sql
var generationAttemptInsertQuery string

// GenerationAttemptInsertRequest is the input to [PgGenerationAttemptInsert.Exec].
type GenerationAttemptInsertRequest struct {
	GenerationID uuid.UUID
	// Response is the provider's final answer, as sent.
	Response json.RawMessage
}

// PgGenerationAttemptInsert records the provider's answer to one attempt.
//
// Call it in the same transaction as the settle it belongs to: a settle without its answer loses
// what the attempt consumed.
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

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	entity := &GenerationAttempt{}

	err = db.NewRaw(
		generationAttemptInsertQuery,
		request.GenerationID,
		request.Response,
	).Scan(ctx, entity)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", refused(err)))
	}

	return entity, nil
}
