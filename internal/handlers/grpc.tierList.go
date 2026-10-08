package handlers

import (
	"context"

	"github.com/samber/lo"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// GrpcTierListService is the service dependency of [GrpcTierList].
type GrpcTierListService interface {
	Exec(ctx context.Context) []core.TierCeilings
}

// GrpcTierList is the gRPC handler for the TierList RPC.
type GrpcTierList struct {
	genaiv0.UnimplementedTierListServiceServer

	service GrpcTierListService
}

func NewGrpcTierList(service GrpcTierListService) *GrpcTierList {
	return &GrpcTierList{service: service}
}

// wireTiers maps a Tier onto its wire enum.
var wireTiers = lo.Invert(generationTiers)

func (handler *GrpcTierList) TierList(
	ctx context.Context, _ *genaiv0.TierListRequest,
) (*genaiv0.TierListResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.TierList")
	defer span.End()

	tiers := lo.Map(handler.service.Exec(ctx), func(ceilings core.TierCeilings, _ int) *genaiv0.TierCeilings {
		return &genaiv0.TierCeilings{
			Tier:            wireTiers[ceilings.Tier],
			MaxInputTokens:  ceilings.MaxInputTokens,
			MaxOutputTokens: ceilings.MaxOutputTokens,
		}
	})

	return &genaiv0.TierListResponse{Tiers: tiers}, nil
}
