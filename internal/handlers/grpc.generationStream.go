package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// GrpcGenerationStreamService is the service dependency of [GrpcGenerationStream].
type GrpcGenerationStreamService interface {
	Exec(ctx context.Context, request *core.GenerationStreamRequest) (*core.GenerationStreamResult, error)
}

// GrpcGenerationStream is the gRPC handler for the GenerationStream RPC.
type GrpcGenerationStream struct {
	genaiv0.UnimplementedGenerationStreamServiceServer

	service GrpcGenerationStreamService
}

// NewGrpcGenerationStream returns a new GrpcGenerationStream handler backed by the given service.
func NewGrpcGenerationStream(service GrpcGenerationStreamService) *GrpcGenerationStream {
	return &GrpcGenerationStream{service: service}
}

// GenerationStream sends the provider's events of a streamed generation after the cursor, up to the
// terminal one.
func (handler *GrpcGenerationStream) GenerationStream(
	request *genaiv0.GenerationStreamRequest, stream grpc.ServerStreamingServer[genaiv0.GenerationStreamResponse],
) error {
	ctx, span := otel.Tracer().Start(stream.Context(), "grpc.GenerationStream")
	defer span.End()

	generationID, err := uuid.Parse(request.GetId())
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid generation id")
	}

	ownerID, err := uuid.Parse(request.GetOwnerId())
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid owner id")
	}

	result, err := handler.service.Exec(ctx, &core.GenerationStreamRequest{
		ID: generationID, OwnerID: ownerID, StartingAfter: request.StartingAfter,
	})
	if err != nil {
		return streamStatus(span, ctx.Err(), err)
	}

	for event, err := range result.Events {
		if err != nil {
			return streamStatus(span, ctx.Err(), err)
		}

		err = stream.Send(&genaiv0.GenerationStreamResponse{SequenceNumber: event.Sequence, Event: event.Data})
		if err != nil {
			// The caller left; it resumes after the last event it received.
			return err
		}
	}

	return nil
}

// streamStatus maps what ended a stream to the status its caller acts on. left is the stream
// context's error.
func streamStatus(span trace.Span, left, err error) error {
	switch {
	// The caller left, which cut the provider's stream too: no server fault.
	case left != nil:
		return status.FromContextError(left).Err()
	// Another owner's generation reports this too, so an id cannot be probed for existence.
	case errors.Is(err, core.ErrGenerationNotFound):
		return status.Error(codes.NotFound, "generation not found")
	case errors.Is(err, core.ErrInvalidRequest):
		return status.Error(codes.InvalidArgument, "invalid request")
	case errors.Is(err, core.ErrGenerationNotStreamed):
		return status.Error(codes.FailedPrecondition, "generation not submitted for streaming")
	case errors.Is(err, core.ErrStreamUnavailable):
		return status.Error(codes.FailedPrecondition, "the provider does not stream this generation now: poll it")
	case errors.Is(err, core.ErrModeUnsupported):
		return status.Error(codes.Unimplemented, "the provider does not support the Streaming mode")
	case errors.Is(err, core.ErrProviderRetryable):
		// The provider dropped the stream, a server fault: a new one resumes after the last event received.
		return grpcf.Fault(
			codes.Unavailable, "stream interrupted: resume after the last event received", otel.ReportError(span, err),
		)
	}

	return grpcf.Fault(codes.Internal, "internal error", otel.ReportError(span, err))
}
