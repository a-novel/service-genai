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

// ItemUpdateService applies the request's changes to the identified item and
// returns the updated record. The core layer supplies the implementation.
type ItemUpdateService interface {
	Exec(ctx context.Context, request *core.ItemUpdateRequest) (*core.Item, error)
}

// ItemUpdate is the gRPC handler for the ItemUpdate RPC.
type ItemUpdate struct {
	protogen.UnimplementedItemUpdateServiceServer

	service ItemUpdateService
}

func NewItemUpdate(service ItemUpdateService) *ItemUpdate {
	return &ItemUpdate{service: service}
}

// ItemUpdate applies the requested changes to the item with the given ID and
// returns the updated record. A malformed ID or rejected request maps to
// InvalidArgument, a missing item to NotFound, and any other failure to Internal.
func (handler *ItemUpdate) ItemUpdate(
	ctx context.Context, request *protogen.ItemUpdateRequest,
) (*protogen.ItemUpdateResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.ItemUpdate")
	defer span.End()

	id, err := uuid.Parse(request.GetId())
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid item id")
	}

	item, err := handler.service.Exec(ctx, &core.ItemUpdateRequest{
		ID:          id,
		Name:        request.GetName(),
		Description: request.GetDescription(),
	})
	if errors.Is(err, core.ErrInvalidRequest) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if errors.Is(err, dao.ErrItemUpdateNotFound) {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.NotFound, "item not found")
	}

	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Internal, "internal error")
	}

	return &protogen.ItemUpdateResponse{Item: itemToProto(item)}, nil
}
