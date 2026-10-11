package dao

import (
	"context"
	_ "embed"
	"fmt"

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
}

// GenerationLiveCount is how many generations an owner has pending or running.
type GenerationLiveCount struct {
	Count int64 `bun:"count"`
}

// PgGenerationCountLive counts an owner's pending and running generations.
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

	err = tx.NewRaw(generationCountLiveQuery, request.OwnerID, request.Purpose).Scan(ctx, live)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return live, nil
}
