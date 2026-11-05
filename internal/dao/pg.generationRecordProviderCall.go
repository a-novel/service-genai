package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationRecordProviderCall.sql
var generationRecordProviderCallQuery string

// GenerationRecordProviderCallRequest attaches a provider operation to the generation that started it.
type GenerationRecordProviderCallRequest struct {
	ID uuid.UUID
	// ProviderCallID is the provider's identifier for the accepted operation.
	ProviderCallID string
}

// PgGenerationRecordProviderCall records the provider operation a generation's start began.
//
// [ErrGenerationChanged] means the generation settled while the start was in flight: the
// accepted operation belongs to nothing and the caller stops it.
type PgGenerationRecordProviderCall struct{}

// NewPgGenerationRecordProviderCall returns a new PgGenerationRecordProviderCall dao.
func NewPgGenerationRecordProviderCall() *PgGenerationRecordProviderCall {
	return &PgGenerationRecordProviderCall{}
}

func (dao *PgGenerationRecordProviderCall) Exec(
	ctx context.Context, request *GenerationRecordProviderCallRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationRecordProviderCall")
	defer span.End()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	entity := &Generation{}

	err = db.NewRaw(
		generationRecordProviderCallQuery, request.ID, request.ProviderCallID,
	).Scan(ctx, entity)
	if errors.Is(err, sql.ErrNoRows) {
		err = errors.Join(err, ErrGenerationChanged)
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
