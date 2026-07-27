package handlers

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

// ItemListService returns a page of items bounded by the request's limit and
// offset. The core layer supplies the implementation.
type ItemListService interface {
	Exec(ctx context.Context, request *core.ItemListRequest) ([]*core.Item, error)
}

// ItemList is the gRPC handler for the ItemList RPC.
type ItemList struct {
	protogen.UnimplementedItemListServiceServer

	service ItemListService
}

func NewItemList(service ItemListService) *ItemList {
	return &ItemList{service: service}
}

// ItemList returns a page of items. A rejected request maps to InvalidArgument;
// any other failure maps to Internal.
func (handler *ItemList) ItemList(
	ctx context.Context, request *protogen.ItemListRequest,
) (*protogen.ItemListResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.ItemList")
	defer span.End()

	items, err := handler.service.Exec(ctx, &core.ItemListRequest{
		Limit:  int(request.GetLimit()),
		Offset: int(request.GetOffset()),
	})
	if errors.Is(err, core.ErrInvalidRequest) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	protoItems := make([]*protogen.Item, len(items))
	for i, item := range items {
		protoItems[i] = itemToProto(item)
	}

	return &protogen.ItemListResponse{Items: protoItems}, nil
}
