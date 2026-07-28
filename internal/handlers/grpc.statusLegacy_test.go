package handlers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel/service-genai/internal/handlers"
	genaiv1 "github.com/a-novel/service-genai/internal/handlers/protogen/genai/v1"
)

type legacyStatusServiceRegistrar struct {
	description    *grpc.ServiceDesc
	implementation any
}

func (registrar *legacyStatusServiceRegistrar) RegisterService(
	description *grpc.ServiceDesc,
	implementation any,
) {
	registrar.description = description
	registrar.implementation = implementation
}

type legacyStatusServiceServer struct {
	genaiv1.UnimplementedStatusServiceServer

	status func(context.Context, *genaiv1.StatusRequest) (*genaiv1.StatusResponse, error)
}

func (server *legacyStatusServiceServer) Status(
	ctx context.Context,
	request *genaiv1.StatusRequest,
) (*genaiv1.StatusResponse, error) {
	return server.status(ctx, request)
}

func TestRegisterLegacyStatusServiceServer(t *testing.T) {
	t.Parallel()

	errFoo := errors.New("foo")

	testCases := []struct {
		name string

		decodeErr      error
		invalidServer  bool
		intercept      bool
		invalidRequest bool

		expectCode  codes.Code
		expectError error
		expectCalls int
	}{
		{
			name: "Success",

			expectCode:  codes.OK,
			expectCalls: 1,
		},
		{
			name: "Success/Interceptor",

			intercept:   true,
			expectCode:  codes.OK,
			expectCalls: 1,
		},
		{
			name: "Error/Decode",

			decodeErr:   errFoo,
			expectError: errFoo,
		},
		{
			name: "Error/InvalidServer",

			invalidServer: true,
			expectCode:    codes.Internal,
		},
		{
			name: "Error/InvalidRequest",

			intercept:      true,
			invalidRequest: true,
			expectCode:     codes.Internal,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			response := &genaiv1.StatusResponse{
				Queue: &genaiv1.QueueDepth{Pending: 3},
			}
			calls := 0
			server := &legacyStatusServiceServer{
				status: func(
					_ context.Context,
					_ *genaiv1.StatusRequest,
				) (*genaiv1.StatusResponse, error) {
					calls++

					return response, nil
				},
			}
			registrar := &legacyStatusServiceRegistrar{}

			handlers.RegisterLegacyStatusServiceServer(registrar, server)

			require.Equal(t, "StatusService", registrar.description.ServiceName)
			require.Equal(t, "genai/v1/status.proto", registrar.description.Metadata)
			require.Len(t, registrar.description.Methods, 1)
			require.Equal(t, "Status", registrar.description.Methods[0].MethodName)
			require.Same(t, server, registrar.implementation)

			implementation := registrar.implementation
			if testCase.invalidServer {
				implementation = struct{}{}
			}

			decode := func(request any) error {
				require.IsType(t, &genaiv1.StatusRequest{}, request)

				return testCase.decodeErr
			}

			var interceptor grpc.UnaryServerInterceptor
			if testCase.intercept {
				interceptor = func(
					ctx context.Context,
					request any,
					info *grpc.UnaryServerInfo,
					handler grpc.UnaryHandler,
				) (any, error) {
					require.Equal(t, "/StatusService/Status", info.FullMethod)
					require.Same(t, implementation, info.Server)

					if testCase.invalidRequest {
						request = struct{}{}
					}

					return handler(ctx, request)
				}
			}

			result, err := registrar.description.Methods[0].Handler(
				implementation,
				t.Context(),
				decode,
				interceptor,
			)

			if testCase.expectError != nil {
				require.ErrorIs(t, err, testCase.expectError)
				require.Nil(t, result)
			} else {
				grpcStatus, ok := status.FromError(err)
				require.True(t, ok)
				require.Equal(t, testCase.expectCode, grpcStatus.Code())

				if testCase.expectCode == codes.OK {
					require.Equal(t, response, result)
				} else {
					require.Nil(t, result)
				}
			}

			require.Equal(t, testCase.expectCalls, calls)
		})
	}
}
