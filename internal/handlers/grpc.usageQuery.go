package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

// GrpcUsageQueryService is the service dependency of [GrpcUsageQuery].
type GrpcUsageQueryService interface {
	Exec(ctx context.Context, request *core.UsageQueryRequest) (*core.UsageQueryResult, error)
}

// GrpcUsageQuery is the gRPC handler for the UsageQuery RPC.
type GrpcUsageQuery struct {
	protogen.UnimplementedUsageQueryServiceServer

	service GrpcUsageQueryService
}

func NewGrpcUsageQuery(service GrpcUsageQueryService) *GrpcUsageQuery {
	return &GrpcUsageQuery{service: service}
}

func (handler *GrpcUsageQuery) UsageQuery(
	ctx context.Context, request *protogen.UsageQueryRequest,
) (*protogen.UsageQueryResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.UsageQuery")
	defer span.End()

	ownerID, err := uuid.Parse(request.GetOwnerId())
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid owner id")
	}

	from, err := time.Parse(time.RFC3339, request.GetFrom())
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid from")
	}

	to, err := time.Parse(time.RFC3339, request.GetTo())
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid to")
	}

	result, err := handler.service.Exec(ctx, &core.UsageQueryRequest{
		OwnerID: ownerID,
		From:    from,
		To:      to,
		Purpose: request.GetPurpose(),
		Model:   request.GetModel(),
	})

	if errors.Is(err, core.ErrInvalidRequest) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid usage query")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	groups := make([]*protogen.UsageGroup, 0, len(result.Groups))

	for _, group := range result.Groups {
		groups = append(groups, &protogen.UsageGroup{
			Purpose:           group.Purpose,
			Model:             group.Model,
			InputTokens:       group.InputTokens,
			CachedInputTokens: group.CachedInputTokens,
			OutputTokens:      group.OutputTokens,
			ReasoningTokens:   group.ReasoningTokens,
			Attempts:          group.Attempts,
		})
	}

	return otel.ReportSuccess(span, &protogen.UsageQueryResponse{
		Groups: groups,
		Total: &protogen.UsageTotal{
			InputTokens:       result.Total.InputTokens,
			CachedInputTokens: result.Total.CachedInputTokens,
			OutputTokens:      result.Total.OutputTokens,
			ReasoningTokens:   result.Total.ReasoningTokens,
			Attempts:          result.Total.Attempts,
		},
	}), nil
}
