package handlers

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	genaiv1 "github.com/a-novel/service-genai/internal/handlers/protogen/genai/v1"
)

const legacyStatusFullMethodName = "/StatusService/Status"

// RegisterLegacyStatusServiceServer preserves the v0.1.1 Status RPC path while the protobuf
// descriptor moves under genai.v1. Register it alongside
// [genaiv1.RegisterStatusServiceServer] so both published and namespaced clients can probe the
// same handler.
func RegisterLegacyStatusServiceServer(
	registrar grpc.ServiceRegistrar,
	server genaiv1.StatusServiceServer,
) {
	registrar.RegisterService(&grpc.ServiceDesc{
		ServiceName: "StatusService",
		HandlerType: (*genaiv1.StatusServiceServer)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Status",
				Handler:    legacyStatusHandler,
			},
		},
		Metadata: "genai/v1/status.proto",
	}, server)
}

func legacyStatusHandler(
	server any,
	ctx context.Context,
	decode func(any) error,
	interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	request := new(genaiv1.StatusRequest)

	err := decode(request)
	if err != nil {
		return nil, err
	}

	statusServer, ok := server.(genaiv1.StatusServiceServer)
	if !ok {
		return nil, status.Error(codes.Internal, "invalid legacy status server")
	}

	if interceptor == nil {
		return statusServer.Status(ctx, request)
	}

	info := &grpc.UnaryServerInfo{Server: server, FullMethod: legacyStatusFullMethodName}
	handler := func(ctx context.Context, request any) (any, error) {
		statusRequest, ok := request.(*genaiv1.StatusRequest)
		if !ok {
			return nil, status.Error(codes.Internal, "invalid legacy status request")
		}

		return statusServer.Status(ctx, statusRequest)
	}

	return interceptor(ctx, request, info, handler)
}
