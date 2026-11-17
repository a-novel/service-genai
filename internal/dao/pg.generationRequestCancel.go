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

//go:embed pg.generationRequestCancel.sql
var generationRequestCancelQuery string

// ErrGenerationNotCancellable is returned when the owner has no generation with this id, or it has
// already settled. Both cases share one error, so a caller cannot probe another owner's identifier.
var ErrGenerationNotCancellable = errors.New("generation cannot be cancelled")

// GenerationRequestCancelRequest is the input to [PgGenerationRequestCancel.Exec].
type GenerationRequestCancelRequest struct {
	ID      uuid.UUID
	OwnerID uuid.UUID
}

// PgGenerationRequestCancel marks a generation its owner asked to stop.
type PgGenerationRequestCancel struct{}

// NewPgGenerationRequestCancel returns a new PgGenerationRequestCancel dao.
func NewPgGenerationRequestCancel() *PgGenerationRequestCancel {
	return &PgGenerationRequestCancel{}
}

func (dao *PgGenerationRequestCancel) Exec(
	ctx context.Context, request *GenerationRequestCancelRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationRequestCancel")
	defer span.End()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	entity := &Generation{}

	err = db.NewRaw(generationRequestCancelQuery, request.ID, request.OwnerID).Scan(ctx, entity)
	if errors.Is(err, sql.ErrNoRows) {
		err = errors.Join(err, ErrGenerationNotCancellable)
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
