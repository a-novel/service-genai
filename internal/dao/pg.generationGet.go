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

//go:embed pg.generationGet.sql
var generationGetQuery string

// ErrGenerationGetNotFound is returned by [GenerationGet.Exec] when the owner has no generation
// with the requested ID. It is joined onto the underlying sql.ErrNoRows so callers can branch on it
// with errors.Is.
//
// A generation owned by someone else reports this too, and deliberately: a distinct "forbidden"
// answer would confirm the identifier exists.
var ErrGenerationGetNotFound = errors.New("generation not found")

// GenerationGetRequest is the input to [GenerationGet.Exec].
type GenerationGetRequest struct {
	// ID of the generation to read.
	ID uuid.UUID
	// OwnerID scopes the read. A generation owned by anyone else reports not-found.
	OwnerID uuid.UUID
}

// GenerationGet reads one of an owner's generations by its ID.
type GenerationGet struct{}

func NewGenerationGet() *GenerationGet {
	return new(GenerationGet)
}

func (dao *GenerationGet) Exec(ctx context.Context, request *GenerationGetRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationGet")
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

	err = tx.NewRaw(generationGetQuery, request.ID, request.OwnerID).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationGetNotFound)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return otel.ReportSuccess(span, entity), nil
}
