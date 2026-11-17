package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/a-novel-kit/golib/grpcf"
	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"
	"github.com/a-novel-kit/golib/servicetoken"
	"github.com/a-novel-kit/golib/servicetoken/servicetokentest"

	"github.com/a-novel/service-genai/internal/config"
)

const testCaller = "system:serviceaccount:narrative-engine:rest"

func TestNewServer(t *testing.T) {
	t.Parallel()

	issuer := servicetokentest.NewIssuer(t)

	gate := servicetoken.Config{Issuer: issuer.URL, Audience: "genai", Callers: []string{testCaller}}

	testCases := []struct {
		name string

		serviceToken servicetoken.Config
		// token sends a valid service token with every call.
		token bool
		// call is the RPC the case makes: "unary" or "stream" echo, or "health".
		call string

		expectStatus codes.Code
		expectErr    error
	}{
		{
			// A local stack opts out, and the server takes every call.
			name: "Success/IssuerOptional",

			serviceToken: servicetoken.Config{IssuerOptional: true},
			call:         "unary",

			expectStatus: codes.OK,
		},
		{
			name: "Success/ValidToken",

			serviceToken: gate,
			token:        true,
			call:         "unary",

			expectStatus: codes.OK,
		},
		{
			// The kubelet probes the health service without a token.
			name: "Success/HealthWithoutToken",

			serviceToken: gate,
			call:         "health",

			expectStatus: codes.OK,
		},
		{
			name: "Error/Unauthenticated",

			serviceToken: gate,
			call:         "unary",

			expectStatus: codes.Unauthenticated,
		},
		{
			// Streams pass the same gate.
			name: "Error/UnauthenticatedStream",

			serviceToken: gate,
			call:         "stream",

			expectStatus: codes.Unauthenticated,
		},
		{
			// A deployment that forgets the issuer stops at startup rather than admit every caller.
			name: "Error/IssuerRequired",

			expectErr: servicetoken.ErrIssuerRequired,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.AppPresetDefault
			cfg.ServiceToken = testCase.serviceToken
			// The connector refuses an empty key; no call reaches the provider.
			cfg.OpenAI.APIKey = "test-key"

			server, err := newServer(t.Context(), cfg, grpcf.NewCallLimit(0))
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				return
			}

			var options []grpc.DialOption

			if testCase.token {
				tokenPath := filepath.Join(t.TempDir(), "token")
				require.NoError(t, os.WriteFile(tokenPath, []byte(issuer.Sign(t, issuer.Claims(testCaller, "genai"))), 0o600))

				options = grpcf.ClientConfig{TokenPath: tokenPath}.DialOptions()
			}

			err = callServer(t.Context(), serve(t, server, options...), testCase.call)
			require.Equal(t, testCase.expectStatus, status.Code(err), "got %v", err)
		})
	}
}

// serve serves server over an in-memory listener, and returns a connection to it dialed with options.
// The test's cleanup stops both.
func serve(t *testing.T, server *grpc.Server, options ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(1 << 16)

	go func() { _ = server.Serve(listener) }()

	t.Cleanup(server.Stop)

	options = append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	}, options...)

	conn, err := grpc.NewClient("passthrough:///bufconn", options...)
	if err != nil {
		panic(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// callServer makes the call a case names: golib's echo service, which every server registers, through
// a unary call or a server stream, or the health service, which must report SERVING.
func callServer(ctx context.Context, conn *grpc.ClientConn, call string) error {
	switch call {
	case "health":
		response, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
		if err == nil && response.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			return status.Error(codes.Unavailable, response.GetStatus().String())
		}

		return err
	case "stream":
		stream, err := golibproto.NewEchoServiceClient(conn).
			ServerStreamingEcho(ctx, &golibproto.ServerStreamingEchoRequest{})
		if err != nil {
			return err
		}

		_, err = stream.Recv()

		return err
	default:
		_, err := golibproto.NewEchoServiceClient(conn).UnaryEcho(ctx, &golibproto.UnaryEchoRequest{})

		return err
	}
}
