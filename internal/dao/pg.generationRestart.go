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

//go:embed pg.generationRestart.sql
var generationRestartQuery string

// GenerationRestartRequest identifies the attempt to discard.
type GenerationRestartRequest struct {
	ID      uuid.UUID
	Attempt int16
	// ProviderCallID is the attempt's operation on the older provider, nil when its start never
	// recorded one.
	ProviderCallID *string
	// ProviderEpoch is the configuration the generation restarts on.
	ProviderEpoch int32
}

// GenerationRestart returns a generation whose attempt runs on an older provider configuration to
// the queue, to start again on the caller's.
//
// The older operation is not cancelled: the replica restarting it holds no credentials for it.
type GenerationRestart struct{}

func NewGenerationRestart() *GenerationRestart {
	return &GenerationRestart{}
}

func (dao *GenerationRestart) Exec(ctx context.Context, request *GenerationRestartRequest) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationRestart")
	defer span.End()

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	entity := &Generation{}

	err = tx.NewRaw(
		generationRestartQuery, request.ID, request.Attempt, request.ProviderCallID, request.ProviderEpoch,
	).Scan(ctx, entity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.Join(err, ErrGenerationChanged)
		}

		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	return entity, nil
}
