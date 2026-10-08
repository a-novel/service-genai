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

// GrpcGenerationSubmitService is the service dependency of [GrpcGenerationSubmit].
type GrpcGenerationSubmitService interface {
	Exec(ctx context.Context, request *core.GenerationSubmitRequest) (*core.GenerationSubmitResult, error)
}

// GrpcGenerationSubmit is the gRPC handler for the GenerationSubmit RPC.
type GrpcGenerationSubmit struct {
	genaiv0.UnimplementedGenerationSubmitServiceServer

	service GrpcGenerationSubmitService
}

func NewGrpcGenerationSubmit(service GrpcGenerationSubmitService) *GrpcGenerationSubmit {
	return &GrpcGenerationSubmit{service: service}
}

func (handler *GrpcGenerationSubmit) GenerationSubmit(
	ctx context.Context, request *genaiv0.GenerationSubmitRequest,
) (*genaiv0.GenerationSubmitResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.GenerationSubmit")
	defer span.End()

	ownerID, err := uuid.Parse(request.GetOwnerId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid owner id")
	}

	result, err := handler.service.Exec(ctx, &core.GenerationSubmitRequest{
		OwnerID:      ownerID,
		Purpose:      request.GetPurpose(),
		Tier:         generationTiers[request.GetTier()],
		Instructions: request.GetInstructions(),
		Input:        request.GetInput(),
		OutputSchema: request.GetOutputSchema(),
		Variant:      request.GetVariant(),
	})

	// The reason names the caller's own fields and limits, so it tells the caller what to fix.
	if errors.Is(err, core.ErrInvalidRequest) {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	return &genaiv0.GenerationSubmitResponse{
		Generation: NewGrpcGeneration(result.Generation),
		Created:    result.Created,
	}, nil
}
