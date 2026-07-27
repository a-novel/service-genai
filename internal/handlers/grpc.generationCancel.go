package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

// GrpcGenerationCancelService is the service dependency of [GrpcGenerationCancel].
type GrpcGenerationCancelService interface {
	Exec(ctx context.Context, request *core.GenerationCancelRequest) (*dao.Generation, error)
}

// GrpcGenerationCancel is the gRPC handler for the GenerationCancel RPC.
type GrpcGenerationCancel struct {
	protogen.UnimplementedGenerationCancelServiceServer

	service GrpcGenerationCancelService
}

func NewGrpcGenerationCancel(service GrpcGenerationCancelService) *GrpcGenerationCancel {
	return &GrpcGenerationCancel{service: service}
}

func (handler *GrpcGenerationCancel) GenerationCancel(
	ctx context.Context, request *protogen.GenerationCancelRequest,
) (*protogen.GenerationCancelResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.GenerationCancel")
	defer span.End()

	generationID, err := uuid.Parse(request.GetId())
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid generation id")
	}

	ownerID, err := uuid.Parse(request.GetOwnerId())
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid owner id")
	}

	generation, err := handler.service.Exec(ctx, &core.GenerationCancelRequest{
		ID: generationID, OwnerID: ownerID,
	})

	// A generation that has already settled reports the same thing as one that does not exist. The
	// two cannot be told apart without confirming that somebody else's id is real.
	if errors.Is(err, core.ErrGenerationNotCancellable) {
		return nil, status.Error(codes.NotFound, "generation not found or already settled")
	}

	if errors.Is(err, core.ErrInvalidRequest) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	return otel.ReportSuccess(span, &protogen.GenerationCancelResponse{
		Generation: NewGrpcGeneration(generation),
	}), nil
}
