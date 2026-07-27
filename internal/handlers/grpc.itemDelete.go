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

// ItemDeleteService deletes the item identified by the request and returns the
// removed record. The core layer supplies the implementation.
type ItemDeleteService interface {
	Exec(ctx context.Context, request *core.ItemDeleteRequest) (*core.Item, error)
}

// ItemDelete is the gRPC handler for the ItemDelete RPC.
type ItemDelete struct {
	protogen.UnimplementedItemDeleteServiceServer

	service ItemDeleteService
}

func NewItemDelete(service ItemDeleteService) *ItemDelete {
	return &ItemDelete{service: service}
}

// ItemDelete deletes the item with the given ID and returns the removed record.
// A malformed ID or rejected request maps to InvalidArgument, a missing item to
// NotFound, and any other failure to Internal.
func (handler *ItemDelete) ItemDelete(
	ctx context.Context, request *protogen.ItemDeleteRequest,
) (*protogen.ItemDeleteResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.ItemDelete")
	defer span.End()

	id, err := uuid.Parse(request.GetId())
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid item id")
	}

	item, err := handler.service.Exec(ctx, &core.ItemDeleteRequest{ID: id})
	if errors.Is(err, core.ErrInvalidRequest) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if errors.Is(err, dao.ErrItemDeleteNotFound) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.NotFound, "item not found")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	return &protogen.ItemDeleteResponse{Item: itemToProto(item)}, nil
}
