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

// GrpcMaxReceiveBytes admits a submit whose payload is at [core.PayloadSizeCeiling], with room for
// its envelope, so an oversized payload reaches core validation rather than gRPC's receive limit.
const GrpcMaxReceiveBytes = core.PayloadSizeCeiling + 64<<10

// GrpcGenerationSubmitService is the service dependency of [GrpcGenerationSubmit].
type GrpcGenerationSubmitService interface {
	Exec(ctx context.Context, request *core.GenerationSubmitRequest) (*core.GenerationSubmitResult, error)
}

// GrpcGenerationSubmit is the gRPC handler for the GenerationSubmit RPC.
type GrpcGenerationSubmit struct {
	genaiv0.UnimplementedGenerationSubmitServiceServer

	service GrpcGenerationSubmitService
}

// NewGrpcGenerationSubmit returns a new GrpcGenerationSubmit handler backed by the given service.
func NewGrpcGenerationSubmit(service GrpcGenerationSubmitService) *GrpcGenerationSubmit {
	return &GrpcGenerationSubmit{service: service}
}

// GenerationSubmit records a generation, or finds the one the same request already recorded, and
// returns it.
func (handler *GrpcGenerationSubmit) GenerationSubmit(
	ctx context.Context, request *genaiv0.GenerationSubmitRequest,
) (*genaiv0.GenerationSubmitResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.GenerationSubmit")
	defer span.End()

	ownerID, err := uuid.Parse(request.GetOwnerId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid owner id")
	}

	serviceRequest := &core.GenerationSubmitRequest{
		OwnerID:  ownerID,
		Purpose:  request.GetPurpose(),
		UserTier: request.GetUserTier(),
		Provider: request.GetProvider(),
		Payload:  request.GetPayload(),
		Variant:  request.GetVariant(),
		Stream:   request.GetStream(),
	}

	result, err := handler.service.Exec(ctx, serviceRequest)

	// The reason names the caller's own fields and limits, so it tells the caller what to fix.
	if errors.Is(err, core.ErrInvalidRequest) {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// The message names the cap, so the caller can tell its user which limit was reached.
	if errors.Is(err, core.ErrUsageCapReached) {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}

	if errors.Is(err, core.ErrModeUnsupported) {
		return nil, status.Error(codes.Unimplemented, "the provider does not support the requested mode")
	}

	if err != nil {
		return nil, grpcf.Fault(codes.Internal, "internal error", otel.ReportError(span, err))
	}

	return &genaiv0.GenerationSubmitResponse{
		Generation: NewGrpcGeneration(result.Generation),
		Created:    result.Created,
	}, nil
}
