package handlers_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/a-novel-kit/golib/grpcf"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/handlers"
)

// heldService holds each call to test.Held/Hold until the test ends it, and answers test.Held/Pass at
// once.
var heldService = &grpc.ServiceDesc{
	ServiceName: "test.Held",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Hold", Handler: holdCall},
		{MethodName: "Pass", Handler: passCall},
	},
}

// heldCalls reports each call test.Held/Hold starts, and ends them all once release closes.
type heldCalls struct {
	started chan struct{}
	release chan struct{}
}

// holdCall runs test.Held/Hold until heldCalls releases it or its connection closes.
func holdCall(srv any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	err := decode(&emptypb.Empty{})
	if err != nil {
		return nil, err
	}

	calls := srv.(*heldCalls)
	calls.started <- struct{}{}

	select {
	case <-calls.release:
	case <-ctx.Done():
	}

	return &emptypb.Empty{}, nil
}

// passCall answers test.Held/Pass at once.
func passCall(_ any, _ context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	return &emptypb.Empty{}, decode(&emptypb.Empty{})
}

func TestGrpcServerOptions(t *testing.T) {
	t.Parallel()

	limit := config.AppPresetDefault.Grpc.MaxConcurrentCalls
	require.Positive(t, limit, "the server bounds its calls by default")

	testCases := []struct {
		name string

		held int

		expectCode codes.Code
	}{
		{
			name: "Success/UnderLimit",

			held: limit - 1,

			expectCode: codes.OK,
		},
		{
			name: "Error/OverLimit",

			held: limit,

			expectCode: codes.Unavailable,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			calls := &heldCalls{started: make(chan struct{}, testCase.held), release: make(chan struct{})}

			server := grpc.NewServer(handlers.GrpcServerOptions(grpcf.NewCallLimit(limit))...)
			server.RegisterService(heldService, calls)

			listener := bufconn.Listen(1 << 16)
			go func() { _ = server.Serve(listener) }()

			t.Cleanup(server.Stop)

			connection, err := grpc.NewClient(
				"passthrough:///bufconn",
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
					return listener.DialContext(ctx)
				}),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connection.Close()) })

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			t.Cleanup(cancel)

			results := make(chan error, testCase.held)

			for range testCase.held {
				go func() { results <- connection.Invoke(ctx, "/test.Held/Hold", &emptypb.Empty{}, &emptypb.Empty{}) }()
			}

			for range testCase.held {
				select {
				case <-calls.started:
				case <-ctx.Done():
					require.FailNow(t, "held calls did not start")
				}
			}

			err = connection.Invoke(ctx, "/test.Held/Pass", &emptypb.Empty{}, &emptypb.Empty{})
			require.Equal(t, testCase.expectCode, status.Code(err))

			close(calls.release)

			for range testCase.held {
				select {
				case err = <-results:
					require.NoError(t, err)
				case <-ctx.Done():
					require.FailNow(t, "held calls did not end")
				}
			}
		})
	}
}
