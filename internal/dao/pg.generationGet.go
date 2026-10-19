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

//go:embed pg.generationGet.sql
var generationGetQuery string

// ErrGenerationGetNotFound is returned by [PgGenerationGet.Exec] when the owner has no generation
// with the requested ID. Another owner's generation reports this too, so an identifier cannot be
// probed for existence.
var ErrGenerationGetNotFound = errors.New("generation not found")

// GenerationGetRequest is the input to [PgGenerationGet.Exec].
type GenerationGetRequest struct {
	ID uuid.UUID
	// OwnerID scopes the read. A generation owned by anyone else reports not-found.
	OwnerID uuid.UUID
}

// PgGenerationGet reads one of an owner's generations by its ID.
type PgGenerationGet struct{}

// NewPgGenerationGet returns a new PgGenerationGet dao.
func NewPgGenerationGet() *PgGenerationGet {
	return &PgGenerationGet{}
}

func (dao *PgGenerationGet) Exec(ctx context.Context, request *GenerationGetRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationGet")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(generationGetQuery, request.ID, request.OwnerID).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationGetNotFound)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
