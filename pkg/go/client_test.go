package servicegenai_test

import (
	"context"
	"net"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/a-novel-kit/golib/grpcf"
	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"

	"github.com/a-novel/service-genai/internal/config/env"
	servicegenai "github.com/a-novel/service-genai/pkg/go"
)

// newClient dials the service this test runs against. It is a live container, not a mock: what is
// checked here is that the published contract works end to end.
func newClient(t *testing.T) servicegenai.Client {
	t.Helper()

	client, err := servicegenai.NewClient(env.GrpcUrl, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}

	t.Cleanup(client.Close)

	return client
}

// submitRequest is a valid submission for the owner.
func submitRequest(owner string) *servicegenai.GenerationSubmitRequest {
	return &servicegenai.GenerationSubmitRequest{
		OwnerId: owner, Purpose: "studio.generation", Provider: "openai",
		Payload: []byte(`{"model": "a-model", "input": "Continue the scene."}`),
	}
}

// submit records a generation for the owner, returning it.
func submit(t *testing.T, client servicegenai.Client, owner string) *servicegenai.Generation {
	t.Helper()

	response, err := client.GenerationSubmit(t.Context(), submitRequest(owner))
	if err != nil {
		panic(err)
	}

	return response.GetGeneration()
}

func TestClient(t *testing.T) {
	t.Parallel()

	t.Run("Success/RemoteCalls", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		_, err := client.UnaryEcho(t.Context(), &golibproto.UnaryEchoRequest{})
		require.NoError(t, err)

		response, err := client.Status(t.Context(), &servicegenai.StatusRequest{})
		require.NoError(t, err)
		require.NotNil(t, response.GetPostgres())
		require.NotNil(t, response.GetQueue())
	})

	t.Run("Success/PropagatesTraceContext", func(t *testing.T) {
		t.Parallel()

		// golib's otel.Init installs this propagator in every service; the default one sends nothing.
		otel.SetTextMapPropagator(propagation.TraceContext{})

		received := make(chan []string, 1)
		server := grpc.NewServer(grpc.UnaryInterceptor(func(
			ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
		) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			received <- md.Get("traceparent")

			return handler(ctx, req)
		}))
		grpcf.RegisterEchoServers(server)

		listener := bufconn.Listen(1 << 16)
		go func() { _ = server.Serve(listener) }()

		defer server.Stop()

		client, err := servicegenai.NewClient(
			"passthrough:///bufconn",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		require.NoError(t, err)

		defer client.Close()

		caller := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    trace.TraceID{1},
			SpanID:     trace.SpanID{1},
			TraceFlags: trace.FlagsSampled,
		})

		_, err = client.UnaryEcho(
			trace.ContextWithSpanContext(t.Context(), caller), &golibproto.UnaryEchoRequest{},
		)
		require.NoError(t, err)

		traceparent := <-received
		require.Len(t, traceparent, 1)
		require.Contains(t, traceparent[0], caller.TraceID().String())
	})

	// The submit contract against a running service: a resend of the same request replays it, any other
	// request creates, and a cancelled generation is rerun rather than served.
	t.Run("GenerationSubmit", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()

		created, err := client.GenerationSubmit(t.Context(), submitRequest(owner))
		require.NoError(t, err)
		require.True(t, created.GetCreated())

		// A caller that lost the answer resends the request: it holds no key, and pays nothing more.
		replayed, err := client.GenerationSubmit(t.Context(), submitRequest(owner))
		require.NoError(t, err)
		require.False(t, replayed.GetCreated())
		require.Equal(t, created.GetGeneration().GetId(), replayed.GetGeneration().GetId())

		// Another variant of the same request is another generation.
		regenerate := submitRequest(owner)
		regenerate.Variant = 1
		variant, err := client.GenerationSubmit(t.Context(), regenerate)
		require.NoError(t, err)
		require.True(t, variant.GetCreated())

		// A cancelled generation is not served again: resending its request runs it from scratch.
		_, err = client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
			Id: created.GetGeneration().GetId(), OwnerId: owner,
		})
		require.NoError(t, err)

		rerun, err := client.GenerationSubmit(t.Context(), submitRequest(owner))
		require.NoError(t, err)
		require.True(t, rerun.GetCreated())
		require.NotEqual(t, created.GetGeneration().GetId(), rerun.GetGeneration().GetId())

		// A purpose the caps do not declare cannot be spent on: the embedded caps reach the image.
		undeclared := submitRequest(owner)
		undeclared.Purpose = "studio.undeclared"
		_, err = client.GenerationSubmit(t.Context(), undeclared)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	// The ownership predicate over the wire. Another owner's generation must be indistinguishable from
	// one that does not exist, or an identifier can be probed.
	t.Run("GenerationGet", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()
		generation := submit(t, client, owner)

		read, err := client.GenerationGet(t.Context(), &servicegenai.GenerationGetRequest{
			Id: generation.GetId(), OwnerId: owner,
		})
		require.NoError(t, err)
		require.Equal(t, generation.GetId(), read.GetGeneration().GetId())
		require.Equal(t, "openai", read.GetGeneration().GetProvider())

		_, err = client.GenerationGet(t.Context(), &servicegenai.GenerationGetRequest{
			Id: generation.GetId(), OwnerId: uuid.Must(uuid.NewV7()).String(),
		})
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	// A generation whose start never reached the provider is cancelled at once: there is no call to stop.
	t.Run("GenerationCancel", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()
		generation := submit(t, client, owner)

		cancelled, err := client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
			Id: generation.GetId(), OwnerId: owner,
		})
		require.NoError(t, err)
		require.Equal(t, servicegenai.GenerationStatusCancelled, cancelled.GetGeneration().GetStatus())

		// A retry after a lost answer succeeds, with the generation as it stands.
		again, err := client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
			Id: generation.GetId(), OwnerId: owner,
		})
		require.NoError(t, err)
		require.Equal(t, servicegenai.GenerationStatusCancelled, again.GetGeneration().GetStatus())

		// Another owner cannot stop it, and is told the same thing as if it did not exist.
		_, err = client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
			Id: generation.GetId(), OwnerId: uuid.Must(uuid.NewV7()).String(),
		})
		require.Equal(t, codes.NotFound, status.Code(err))
	})
}
