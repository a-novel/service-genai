package handlers_test

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/a-novel-kit/golib/grpcf"
	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"
	"github.com/a-novel-kit/golib/servicetoken"
	"github.com/a-novel-kit/golib/servicetoken/servicetokentest"

	"github.com/a-novel/service-genai/internal/handlers"
)

const (
	// serviceTokenAudience is the audience the service token tests answer to.
	serviceTokenAudience = "service-genai"
	// serviceTokenCaller is the one caller the service token tests allow.
	serviceTokenCaller = "system:serviceaccount:agora:caller"
	// serviceTokenStranger is a caller outside the allowed ones.
	serviceTokenStranger = "system:serviceaccount:agora:stranger"
)

func TestGrpcServiceToken(t *testing.T) {
	t.Parallel()

	issuer := servicetokentest.NewIssuer(t)
	tokenConfig := servicetoken.Config{
		Issuer:   issuer.URL,
		Audience: serviceTokenAudience,
		Callers:  []string{serviceTokenCaller},
	}

	testCases := []struct {
		name string

		tokenConfig servicetoken.Config
		token       string
		stream      bool

		expectStatus codes.Code
	}{
		{
			name: "Success",

			tokenConfig: tokenConfig,
			token:       issuer.Sign(t, issuer.Claims(serviceTokenCaller, serviceTokenAudience)),

			expectStatus: codes.OK,
		},
		{
			name: "Success/Unconfigured",

			expectStatus: codes.OK,
		},
		{
			name: "Error/UnknownCaller",

			tokenConfig: tokenConfig,
			token:       issuer.Sign(t, issuer.Claims(serviceTokenStranger, serviceTokenAudience)),

			expectStatus: codes.Unauthenticated,
		},
		{
			name: "Error/Missing",

			tokenConfig: tokenConfig,

			expectStatus: codes.Unauthenticated,
		},
		{
			name: "Error/Stream",

			tokenConfig: tokenConfig,
			stream:      true,

			expectStatus: codes.Unauthenticated,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			conn := serveEcho(t, handlers.GrpcServiceToken(testCase.tokenConfig, &http.Client{}))

			ctx := t.Context()
			if testCase.token != "" {
				ctx = metadata.AppendToOutgoingContext(ctx, servicetoken.Header, testCase.token)
			}

			err := callEcho(ctx, conn, testCase.stream)
			require.Equal(t, testCase.expectStatus, status.Code(err), "got %v", err)
		})
	}
}

// serveEcho serves golib's echo service with options over an in-memory listener, and returns a
// connection to it. The test's cleanup stops both.
func serveEcho(t *testing.T, options []grpc.ServerOption) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(options...)
	grpcf.RegisterEchoServers(server)

	go func() { _ = server.Serve(listener) }()

	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///service-token-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	if err != nil {
		panic(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// callEcho calls the echo service on conn, through a unary call or a server stream.
func callEcho(ctx context.Context, conn *grpc.ClientConn, stream bool) error {
	client := golibproto.NewEchoServiceClient(conn)

	if !stream {
		_, err := client.UnaryEcho(ctx, &golibproto.UnaryEchoRequest{})

		return err
	}

	echoStream, err := client.ServerStreamingEcho(ctx, &golibproto.ServerStreamingEchoRequest{})
	if err != nil {
		return err
	}

	_, err = echoStream.Recv()

	return err
}
