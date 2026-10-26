package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/grpcf"
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

// NewGrpcGenerationGet returns a new GrpcGenerationGet handler backed by the given service.
func NewGrpcGenerationGet(service GrpcGenerationGetService) *GrpcGenerationGet {
	return &GrpcGenerationGet{service: service}
}

// GenerationGet returns one of an owner's generations, checked with the provider first when stale.
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

	if errors.Is(err, core.ErrInvalidRequest) {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if err != nil {
		return nil, grpcf.Fault(codes.Internal, "internal error", otel.ReportError(span, err))
	}

	return &genaiv0.GenerationGetResponse{
		Generation: NewGrpcGeneration(generation),
	}, nil
}
