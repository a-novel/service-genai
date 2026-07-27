package handlers

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

// ItemCreateService creates an item from the request and returns the stored
// record. The core layer supplies the implementation.
type ItemCreateService interface {
	Exec(ctx context.Context, request *core.ItemCreateRequest) (*core.Item, error)
}

// ItemCreate is the gRPC handler for the ItemCreate RPC.
type ItemCreate struct {
	protogen.UnimplementedItemCreateServiceServer

	service ItemCreateService
}

func NewItemCreate(service ItemCreateService) *ItemCreate {
	return &ItemCreate{service: service}
}

// ItemCreate creates an item and returns it. A rejected request maps to
// InvalidArgument; any other failure maps to Internal.
func (handler *ItemCreate) ItemCreate(
	ctx context.Context, request *protogen.ItemCreateRequest,
) (*protogen.ItemCreateResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.ItemCreate")
	defer span.End()

	item, err := handler.service.Exec(ctx, &core.ItemCreateRequest{
		Name:        request.GetName(),
		Description: request.GetDescription(),
	})
	if errors.Is(err, core.ErrInvalidRequest) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	return &protogen.ItemCreateResponse{Item: itemToProto(item)}, nil
}

// itemToProto converts a core item into its protobuf form, encoding timestamps
// as RFC 3339 strings.
func itemToProto(item *core.Item) *protogen.Item {
	return &protogen.Item{
		Id:          item.ID.String(),
		Name:        item.Name,
		Description: item.Description,
		CreatedAt:   item.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   item.UpdatedAt.Format(time.RFC3339),
	}
}
