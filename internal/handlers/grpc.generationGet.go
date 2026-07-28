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
	genaiv1 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v1"
)

// GrpcGenerationGetService is the service dependency of [GrpcGenerationGet].
type GrpcGenerationGetService interface {
	Exec(ctx context.Context, request *core.GenerationGetRequest) (*dao.Generation, error)
}

// GrpcGenerationGet is the gRPC handler for the GenerationGet RPC.
type GrpcGenerationGet struct {
	genaiv1.UnimplementedGenerationGetServiceServer

	service GrpcGenerationGetService
}

func NewGrpcGenerationGet(service GrpcGenerationGetService) *GrpcGenerationGet {
	return &GrpcGenerationGet{service: service}
}

func (handler *GrpcGenerationGet) GenerationGet(
	ctx context.Context, request *genaiv1.GenerationGetRequest,
) (*genaiv1.GenerationGetResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.GenerationGet")
	defer span.End()

	generation, err := handler.read(ctx, request.GetId(), request.GetOwnerId())
	if err != nil {
		return nil, err
	}

	return otel.ReportSuccess(span, &genaiv1.GenerationGetResponse{
		Generation: NewGrpcGeneration(generation),
	}), nil
}

// read is shared with the watch handler, which needs the same lookup and the same refusals.
func (handler *GrpcGenerationGet) read(ctx context.Context, id, owner string) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.GenerationGet(read)")
	defer span.End()

	generationID, err := uuid.Parse(id)
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid generation id")
	}

	ownerID, err := uuid.Parse(owner)
	if err != nil {
		_ = otel.ReportError(span, err)

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

	return otel.ReportSuccess(span, generation), nil
}
