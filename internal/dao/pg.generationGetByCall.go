package dao

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationGetByCall.sql
var generationGetByCallQuery string

// ErrGenerationGetByCallNotFound is returned when no running generation carries the provider call.
var ErrGenerationGetByCallNotFound = errors.New("no running generation carries this provider call")

// GenerationGetByCallRequest is the input to [PgGenerationGetByCall.Exec].
type GenerationGetByCallRequest struct {
	// Provider names the service the call runs on.
	Provider string
	// CallID is the provider's identifier for the call.
	CallID string
}

// PgGenerationGetByCall reads the running generation carrying a provider call. A settled one has
// nothing left to check, so it reports not-found.
type PgGenerationGetByCall struct{}

// NewPgGenerationGetByCall returns a new PgGenerationGetByCall dao.
func NewPgGenerationGetByCall() *PgGenerationGetByCall {
	return &PgGenerationGetByCall{}
}

func (dao *PgGenerationGetByCall) Exec(ctx context.Context, request *GenerationGetByCallRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationGetByCall")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(generationGetByCallQuery, request.Provider, request.CallID).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationGetByCallNotFound)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
