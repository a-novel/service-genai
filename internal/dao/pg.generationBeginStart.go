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

//go:embed pg.generationBeginStart.sql
var generationBeginStartQuery string

// GenerationBeginStartRequest identifies the generation to start.
type GenerationBeginStartRequest struct {
	ID uuid.UUID
}

// GenerationBeginStart takes the next attempt and records that its provider call may be sent.
//
// It refuses with [ErrGenerationChanged] unless the generation is pending, due, not cancelled and
// not already starting, so one check at most sends each attempt.
type GenerationBeginStart struct{}

func NewGenerationBeginStart() *GenerationBeginStart {
	return &GenerationBeginStart{}
}

func (dao *GenerationBeginStart) Exec(ctx context.Context, request *GenerationBeginStartRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationBeginStart")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(generationBeginStartQuery, request.ID).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationChanged)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
