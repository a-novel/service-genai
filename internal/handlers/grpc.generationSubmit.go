package handlers

import (
	"context"
	"errors"
	"math"

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
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid owner id")
	}

	// Range-checked before narrowing. int32 wraps into int16 silently, so a caller sending 65537
	// would arrive as 1 and pass the ceiling check that was supposed to refuse it.
	maxAttempts := request.GetMaxAttempts()
	if maxAttempts < 0 || maxAttempts > math.MaxInt16 {
		return nil, status.Error(codes.InvalidArgument, "invalid max attempts")
	}

	result, err := handler.service.Exec(ctx, &core.GenerationSubmitRequest{
		OwnerID:        ownerID,
		Purpose:        request.GetPurpose(),
		IdempotencyKey: request.GetIdempotencyKey(),
		Request:        request.GetRequest(),
		MaxAttempts:    int16(maxAttempts),
	})

	if errors.Is(err, core.ErrInvalidRequest) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid submission")
	}

	// The key is held by a different request. Answering with the earlier generation would answer a
	// question the caller never asked.
	if errors.Is(err, core.ErrIdempotencyConflict) {
		return nil, status.Error(codes.AlreadyExists, "idempotency key already used with a different request")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	return otel.ReportSuccess(span, &genaiv0.GenerationSubmitResponse{
		Generation: NewGrpcGeneration(result.Generation),
		Created:    result.Created,
	}), nil
}
