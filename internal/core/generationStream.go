package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// ErrGenerationNotStreamed is returned when a stream is asked of a generation not submitted for
// streaming.
var ErrGenerationNotStreamed = errors.New("generation not submitted for streaming")

// GenerationStreamDao reads the generation to stream.
type GenerationStreamDao interface {
	Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
}

// GenerationStreamRequest holds the parameters for a [GenerationStream.Exec] call.
type GenerationStreamRequest struct {
	// ID identifies the generation to stream.
	ID uuid.UUID `validate:"required"`
	// OwnerID scopes the stream to the owner's generations.
	OwnerID uuid.UUID `validate:"required"`
	// StartingAfter is the cursor: the sequence number of the last event the caller received. Nil
	// streams from the first event.
	StartingAfter *int64 `validate:"omitnil,gte=0"`
}

// GenerationStreamEvent is one of the provider's events, as sent.
type GenerationStreamEvent struct {
	// Sequence orders the generation's events; a stream resumes after it.
	Sequence int64
	// Data is the event as the provider sent it.
	Data json.RawMessage
}

// GenerationStreamResult carries the events, read as the caller consumes them.
type GenerationStreamResult struct {
	// Events yields the provider's events until a terminal one. [ErrStreamUnavailable] means the
	// provider no longer streams the generation.
	Events iter.Seq2[*GenerationStreamEvent, error]
}

// A GenerationStream forwards the provider's events of a streamed generation as they are produced.
//
// It never writes: checks remain the only thing that settles a generation, so a stream that drops
// loses nothing, and the caller resumes after its cursor or polls.
type GenerationStream struct {
	dao      GenerationStreamDao
	registry connectors.Registry
}

// NewGenerationStream returns a new GenerationStream service.
func NewGenerationStream(dao GenerationStreamDao, registry connectors.Registry) *GenerationStream {
	return &GenerationStream{dao: dao, registry: registry}
}

func (service *GenerationStream) Exec(
	ctx context.Context, request *GenerationStreamRequest,
) (*GenerationStreamResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationStream")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", request.ID.String()),
		attribute.String("generation.owner_id", request.OwnerID.String()),
	)

	err := validate.Struct(request)
	if err != nil {
		return nil, otel.ReportError(span, errors.Join(err, ErrInvalidRequest))
	}

	generation, err := service.dao.Exec(ctx, &dao.GenerationGetRequest{ID: request.ID, OwnerID: request.OwnerID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get generation: %w", err))
	}

	if !generation.Streamed {
		return nil, otel.ReportError(span, ErrGenerationNotStreamed)
	}

	// A streamed start records its call once the provider has begun it.
	if generation.ProviderCallID == nil {
		return nil, otel.ReportError(span, fmt.Errorf("%w: no provider call yet", ErrStreamUnavailable))
	}

	connector, err := service.registry.Streaming(generation.Provider)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("provider %q: %w", generation.Provider, err))
	}

	streamRequest := &connectors.StreamRequest{ID: *generation.ProviderCallID, StartingAfter: request.StartingAfter}

	return &GenerationStreamResult{Events: func(yield func(*GenerationStreamEvent, error) bool) {
		forwardEvents(ctx, connector, streamRequest, yield)
	}}, nil
}

// forwardEvents yields the provider's events until the terminal one, a failure, or the caller stops.
// It runs after Exec returns, so it takes a span of its own.
func forwardEvents(
	ctx context.Context,
	connector connectors.Streaming,
	request *connectors.StreamRequest,
	yield func(*GenerationStreamEvent, error) bool,
) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationStream(events)")
	defer span.End()

	for event, err := range connector.Stream(ctx, request) {
		if err != nil {
			yield(nil, otel.ReportError(span, err))

			return
		}

		if !yield(&GenerationStreamEvent{Sequence: event.Sequence, Data: event.Data}, nil) {
			return
		}
	}
}
