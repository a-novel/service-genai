package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// GrpcUsageSummaryService is the service dependency of [GrpcUsageSummary].
type GrpcUsageSummaryService interface {
	Exec(ctx context.Context, request *core.UsageSummaryRequest) (*core.UsageSummaryResult, error)
}

// GrpcUsageSummary is the gRPC handler for the UsageSummary RPC.
type GrpcUsageSummary struct {
	genaiv0.UnimplementedUsageSummaryServiceServer

	service GrpcUsageSummaryService
}

// NewGrpcUsageSummary returns a new GrpcUsageSummary handler backed by the given service.
func NewGrpcUsageSummary(service GrpcUsageSummaryService) *GrpcUsageSummary {
	return &GrpcUsageSummary{service: service}
}

// UsageSummary reports an owner's generations in flight for a purpose, and the tokens they consumed
// over each window.
func (handler *GrpcUsageSummary) UsageSummary(
	ctx context.Context, request *genaiv0.UsageSummaryRequest,
) (*genaiv0.UsageSummaryResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.UsageSummary")
	defer span.End()

	ownerID, err := uuid.Parse(request.GetOwnerId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid owner id")
	}

	windows := lo.Map(request.GetWindows(), func(window *durationpb.Duration, _ int) time.Duration {
		return window.AsDuration()
	})

	result, err := handler.service.Exec(ctx, &core.UsageSummaryRequest{
		OwnerID: ownerID, Purpose: request.GetPurpose(), Windows: windows,
	})

	// The reason names the caller's own fields, so it tells the caller what to fix.
	if errors.Is(err, core.ErrInvalidRequest) {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err != nil {
		return nil, grpcf.Fault(codes.Internal, "internal error", otel.ReportError(span, err))
	}

	return &genaiv0.UsageSummaryResponse{
		Live: result.Live,
		Windows: lo.Map(result.Windows, func(window *core.UsageSummaryWindow, _ int) *genaiv0.UsageSummaryWindow {
			return &genaiv0.UsageSummaryWindow{
				Duration:     durationpb.New(window.Duration),
				InputTokens:  window.InputTokens,
				OutputTokens: window.OutputTokens,
			}
		}),
	}, nil
}
