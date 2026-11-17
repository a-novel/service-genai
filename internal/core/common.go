package core

import (
	"context"
	"fmt"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// ErrGenerationNotFound is returned when the owner has no such generation. A generation owned by
// somebody else reports this too, so an identifier cannot be probed for existence.
var ErrGenerationNotFound = dao.ErrGenerationNotFound

// ErrStreamUnavailable is returned when the provider no longer streams a generation, or has not
// begun it yet: its result is read by polling.
var ErrStreamUnavailable = connectors.ErrStreamUnavailable

// ErrModeUnsupported is returned when the provider's connector does not offer the mode a request
// needs.
var ErrModeUnsupported = connectors.ErrModeUnsupported

// ErrProviderRetryable is returned when the provider failed transiently, so the same request may
// succeed again: a stream it dropped resumes after the last event received.
var ErrProviderRetryable = connectors.ErrRetryable

// GenerationAttemptListDao reads the provider's answers to a generation's attempts.
type GenerationAttemptListDao interface {
	Exec(ctx context.Context, request *dao.GenerationAttemptListRequest) ([]*dao.GenerationAttempt, error)
}

// loadGeneration returns the generation in its core form, with the provider's answers. An answer is
// recorded when the generation settles, so an unsettled one is returned without reading any.
func loadGeneration(
	ctx context.Context, daoAttemptList GenerationAttemptListDao, generation *dao.Generation,
) (*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.loadGeneration")
	defer span.End()

	if generation.SettledAt == nil {
		return newGeneration(generation, nil), nil
	}

	attempts, err := daoAttemptList.Exec(ctx, &dao.GenerationAttemptListRequest{GenerationID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("list attempts: %w", err))
	}

	return newGeneration(generation, attempts), nil
}
