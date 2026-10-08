package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationElectCheck.sql
var generationElectCheckQuery string

// ErrGenerationCheckNotDue is returned when the generation is settled, absent for this owner, or
// was checked within the interval.
var ErrGenerationCheckNotDue = errors.New("generation is not due for a check")

// GenerationElectCheckRequest identifies the generation a reader wants checked.
type GenerationElectCheckRequest struct {
	ID      uuid.UUID
	OwnerID uuid.UUID
	// Interval is how long a check stays fresh.
	Interval time.Duration
}

// GenerationElectCheck marks a stale generation checked and returns it, electing the caller to run
// the check.
type GenerationElectCheck struct{}

func NewGenerationElectCheck() *GenerationElectCheck {
	return &GenerationElectCheck{}
}

func (dao *GenerationElectCheck) Exec(
	ctx context.Context, request *GenerationElectCheckRequest,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationElectCheck")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(
		generationElectCheckQuery,
		request.ID,
		request.OwnerID,
		request.Interval.Seconds(),
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationCheckNotDue)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
