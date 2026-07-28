package handlers

import (
	"time"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// GenerationWatchInterval is how often the stream re-reads durable state.
//
// Polling rather than subscribing is what makes the stream resumable: it holds no cursor and no
// buffered events, so a caller that reconnects simply calls again and is answered from current
// state. There is no window in which a change can be missed.
const GenerationWatchInterval = time.Second

// GrpcGenerationWatch is the gRPC handler for the GenerationWatch RPC.
//
// It reads through the get handler, so the ownership predicate and the refusals are the same ones —
// a watch cannot see what a read could not.
type GrpcGenerationWatch struct {
	genaiv0.UnimplementedGenerationWatchServiceServer

	reader   *GrpcGenerationGet
	interval time.Duration
}

func NewGrpcGenerationWatch(reader *GrpcGenerationGet) *GrpcGenerationWatch {
	return &GrpcGenerationWatch{reader: reader, interval: GenerationWatchInterval}
}

func (handler *GrpcGenerationWatch) GenerationWatch(
	request *genaiv0.GenerationWatchRequest,
	stream genaiv0.GenerationWatchService_GenerationWatchServer,
) error {
	ctx, span := otel.Tracer().Start(stream.Context(), "grpc.GenerationWatch")
	defer span.End()

	var last *dao.Generation

	for {
		generation, err := handler.reader.read(ctx, request.GetId(), request.GetOwnerId())
		if err != nil {
			// Already a gRPC status from the shared read.
			return err
		}

		// One snapshot on subscribe, then one per change. Re-sending an unchanged generation every
		// tick would make the stream a poll the caller pays for.
		if last == nil || !generation.UpdatedAt.Equal(last.UpdatedAt) {
			err = stream.Send(&genaiv0.GenerationWatchResponse{
				Generation: NewGrpcGeneration(generation),
			})
			if err != nil {
				return otel.ReportError(span, err)
			}
		}

		if generation.SettledAt != nil {
			// Terminal. The caller has the final state, so the stream ends rather than idling.
			otel.ReportSuccessNoContent(span)

			return nil
		}

		last = generation

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(handler.interval):
		}
	}
}
