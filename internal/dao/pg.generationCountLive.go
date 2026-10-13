package dao

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationCountLive.sql
var generationCountLiveQuery string

// GenerationCountLiveRequest names the owner whose live generations to count.
type GenerationCountLiveRequest struct {
	OwnerID uuid.UUID
	// Purpose restricts the count to one purpose; nil counts every purpose.
	Purpose *string
	// UnknownOutcomeWindow is how long a generation that failed with its start's outcome unknown
	// still counts. Zero counts none.
	UnknownOutcomeWindow time.Duration
}

// GenerationLiveCount is how many generations an owner has in flight.
type GenerationLiveCount struct {
	Count int64 `bun:"count"`
}

// PgGenerationCountLive counts an owner's generations in flight: pending, running, or failed within
// the window with an outcome unknown.
type PgGenerationCountLive struct{}

// NewPgGenerationCountLive returns a new PgGenerationCountLive dao.
func NewPgGenerationCountLive() *PgGenerationCountLive {
	return &PgGenerationCountLive{}
}

func (dao *PgGenerationCountLive) Exec(
	ctx context.Context, request *GenerationCountLiveRequest,
) (*GenerationLiveCount, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationCountLive")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	live := &GenerationLiveCount{}

	err = tx.NewRaw(
		generationCountLiveQuery, request.OwnerID, request.Purpose, request.UnknownOutcomeWindow.Seconds(),
	).Scan(ctx, live)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return live, nil
}
