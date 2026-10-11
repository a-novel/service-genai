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

//go:embed pg.generationPayloadInsert.sql
var generationPayloadInsertQuery string

// GenerationPayloadInsertRequest is the input to [PgGenerationPayloadInsert.Exec].
type GenerationPayloadInsertRequest struct {
	// GenerationID identifies the generation the payload belongs to.
	GenerationID uuid.UUID
	// Payload is the provider's own request, stored as sent.
	Payload json.RawMessage
}

// PgGenerationPayloadInsert stores the payload of a newly admitted generation.
type PgGenerationPayloadInsert struct{}

// NewPgGenerationPayloadInsert returns a new PgGenerationPayloadInsert dao.
func NewPgGenerationPayloadInsert() *PgGenerationPayloadInsert {
	return &PgGenerationPayloadInsert{}
}

func (dao *PgGenerationPayloadInsert) Exec(
	ctx context.Context, request *GenerationPayloadInsertRequest,
) (*GenerationPayload, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationPayloadInsert")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &GenerationPayload{}

	err = tx.NewRaw(generationPayloadInsertQuery, request.GenerationID, request.Payload).Scan(ctx, entity)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
