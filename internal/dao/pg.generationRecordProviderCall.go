package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationRecordProviderCall.sql
var generationRecordProviderCallQuery string

// ErrGenerationInvalidStatus is returned when a transition names a status it cannot land in.
var ErrGenerationInvalidStatus = errors.New("invalid generation status")

// GenerationRecordProviderCallRequest is the input to [GenerationRecordProviderCall.Exec].
type GenerationRecordProviderCallRequest struct {
	ID       uuid.UUID
	WorkerID string
	// ProviderCallID is the provider's own identifier for the operation just started. Recording it
	// is what makes the generation resumable.
	ProviderCallID string
}

// GenerationRecordProviderCall attaches a provider operation to a running generation.
type GenerationRecordProviderCall struct{}

func NewGenerationRecordProviderCall() *GenerationRecordProviderCall {
	return new(GenerationRecordProviderCall)
}

func (dao *GenerationRecordProviderCall) Exec(
	ctx context.Context, request *GenerationRecordProviderCallRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationRecordProviderCall")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.worker_id", request.WorkerID),
	)

	if request.ProviderCallID == "" {
		return nil, otel.ReportError(span, fmt.Errorf("%w: empty provider call id", ErrGenerationNotHeld))
	}

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := new(Generation)

	err = tx.NewRaw(
		generationRecordProviderCallQuery, request.ID, request.WorkerID, request.ProviderCallID,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationNotHeld)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return otel.ReportSuccess(span, entity), nil
}
