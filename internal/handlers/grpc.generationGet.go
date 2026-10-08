package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// GrpcGenerationGetService is the service dependency of [GrpcGenerationGet].
type GrpcGenerationGetService interface {
	Exec(ctx context.Context, request *core.GenerationGetRequest) (*core.Generation, error)
}

// GrpcGenerationGet is the gRPC handler for the GenerationGet RPC.
type GrpcGenerationGet struct {
	genaiv0.UnimplementedGenerationGetServiceServer

	service GrpcGenerationGetService
}

func NewGrpcGenerationGet(service GrpcGenerationGetService) *GrpcGenerationGet {
	return &GrpcGenerationGet{service: service}
}

func (handler *GrpcGenerationGet) GenerationGet(
	ctx context.Context, request *genaiv0.GenerationGetRequest,
) (*genaiv0.GenerationGetResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.GenerationGet")
	defer span.End()

	generationID, err := uuid.Parse(request.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid generation id")
	}

	ownerID, err := uuid.Parse(request.GetOwnerId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid owner id")
	}

	generation, err := handler.service.Exec(ctx, &core.GenerationGetRequest{
		ID: generationID, OwnerID: ownerID,
	})

	// Another owner's generation reports this too, so an id cannot be probed for existence.
	if errors.Is(err, core.ErrGenerationNotFound) {
		return nil, status.Error(codes.NotFound, "generation not found")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	return &genaiv0.GenerationGetResponse{
		Generation: NewGrpcGeneration(generation),
	}, nil
}
