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

// GenerationRecordProviderCallRequest attaches a provider operation to the attempt that started it.
type GenerationRecordProviderCallRequest struct {
	ID      uuid.UUID
	Attempt int16
	// ProviderCallID is the provider's identifier for the accepted operation.
	ProviderCallID string
}

// GenerationRecordProviderCall records the provider operation an attempt started.
//
// [ErrGenerationChanged] means the generation settled while the start was in flight: the
// accepted operation belongs to nothing and the caller stops it.
type GenerationRecordProviderCall struct{}

func NewGenerationRecordProviderCall() *GenerationRecordProviderCall {
	return &GenerationRecordProviderCall{}
}

func (dao *GenerationRecordProviderCall) Exec(
	ctx context.Context, request *GenerationRecordProviderCallRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationRecordProviderCall")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(
		generationRecordProviderCallQuery, request.ID, request.Attempt, request.ProviderCallID,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationChanged)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
