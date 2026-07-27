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

//go:embed pg.generationRequestCancel.sql
var generationRequestCancelQuery string

// ErrGenerationNotCancellable is returned when the owner has no generation with this id, or it has
// already settled. The two are deliberately one error: distinguishing them would tell a caller that
// somebody else's identifier exists.
var ErrGenerationNotCancellable = errors.New("generation cannot be cancelled")

// GenerationRequestCancelRequest is the input to [GenerationRequestCancel.Exec].
type GenerationRequestCancelRequest struct {
	ID      uuid.UUID
	OwnerID uuid.UUID
}

// GenerationRequestCancel marks a generation for cancellation.
type GenerationRequestCancel struct{}

func NewGenerationRequestCancel() *GenerationRequestCancel {
	return new(GenerationRequestCancel)
}

func (dao *GenerationRequestCancel) Exec(
	ctx context.Context, request *GenerationRequestCancelRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationRequestCancel")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.owner_id", request.OwnerID.String()),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := new(Generation)

	err = tx.NewRaw(generationRequestCancelQuery, request.ID, request.OwnerID).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationNotCancellable)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return otel.ReportSuccess(span, entity), nil
}
