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

// ItemGetService returns the item identified by the request. The core layer
// supplies the implementation.
type ItemGetService interface {
	Exec(ctx context.Context, request *core.ItemGetRequest) (*core.Item, error)
}

// ItemGet is the gRPC handler for the ItemGet RPC.
type ItemGet struct {
	protogen.UnimplementedItemGetServiceServer

	service ItemGetService
}

func NewItemGet(service ItemGetService) *ItemGet {
	return &ItemGet{service: service}
}

// ItemGet returns the item with the given ID. A malformed ID or rejected request
// maps to InvalidArgument, a missing item to NotFound, and any other failure to
// Internal.
func (handler *ItemGet) ItemGet(
	ctx context.Context, request *protogen.ItemGetRequest,
) (*protogen.ItemGetResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.ItemGet")
	defer span.End()

	id, err := uuid.Parse(request.GetId())
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid item id")
	}

	item, err := handler.service.Exec(ctx, &core.ItemGetRequest{ID: id})
	if errors.Is(err, core.ErrInvalidRequest) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if errors.Is(err, dao.ErrItemGetNotFound) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.NotFound, "item not found")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	return &protogen.ItemGetResponse{Item: itemToProto(item)}, nil
}
