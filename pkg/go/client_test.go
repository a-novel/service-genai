package servicegenai_test

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/a-novel-kit/golib/grpcf"
	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"

	"github.com/a-novel/service-genai/internal/config/env"
	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
	servicegenai "github.com/a-novel/service-genai/pkg/go"
)

// refuseFirstSubmit refuses the first submit it reads with Unavailable, as a full server does, and
// answers the others with a new generation.
type refuseFirstSubmit struct {
	genaiv0.UnimplementedGenerationSubmitServiceServer

	refused *atomic.Bool
}

func (server refuseFirstSubmit) GenerationSubmit(
	context.Context, *servicegenai.GenerationSubmitRequest,
) (*servicegenai.GenerationSubmitResponse, error) {
	if server.refused.CompareAndSwap(false, true) {
		return nil, status.Error(codes.Unavailable, "server at its concurrent call limit: retry later")
	}

	return &servicegenai.GenerationSubmitResponse{Created: true}, nil
}

// newClient dials the service this test runs against. It is a live container, not a mock: what is
// checked here is that the published contract works end to end.
func newClient(t *testing.T) servicegenai.Client {
	t.Helper()

	client, err := servicegenai.NewClient(env.GrpcURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
		require.NotNil(t, response.GetQueue().GetOldestPendingAge())
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

		callerConfig := trace.SpanContextConfig{
			TraceID:    trace.TraceID{1},
			SpanID:     trace.SpanID{1},
			TraceFlags: trace.FlagsSampled,
		}
		caller := trace.NewSpanContext(callerConfig)

		_, err = client.UnaryEcho(
			trace.ContextWithSpanContext(t.Context(), caller), &golibproto.UnaryEchoRequest{},
		)
		require.NoError(t, err)

		traceparent := <-received
		require.Len(t, traceparent, 1)
		require.Contains(t, traceparent[0], caller.TraceID().String())
	})

	// A full server refuses a submit past gRPC's default retry buffer of 256 KiB, and an in-cluster
	// caller sends it again.
	t.Run("Success/RetriesRefusedSubmit", func(t *testing.T) {
		t.Parallel()

		// The server refuses the first submit once it has read it, so the client has sent the whole
		// request when the refusal arrives.
		var refused atomic.Bool

		server := grpc.NewServer()
		genaiv0.RegisterGenerationSubmitServiceServer(server, refuseFirstSubmit{refused: &refused})

		listener := bufconn.Listen(1 << 16)
		go func() { _ = server.Serve(listener) }()

		defer server.Stop()

		tokenPath := filepath.Join(t.TempDir(), "token")
		require.NoError(t, os.WriteFile(tokenPath, []byte("token"), 0o600))

		provider := &grpcf.ServiceTokenCredentialsProvider{ServiceTokenPath: tokenPath}
		options, err := provider.Options(t.Context())
		require.NoError(t, err)

		client, err := servicegenai.NewClient("passthrough:///bufconn", append(options,
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
		)...)
		require.NoError(t, err)

		defer client.Close()

		request := submitRequest(uuid.Must(uuid.NewV7()).String())
		request.Payload = bytes.Repeat([]byte("x"), 1<<20)

		response, err := client.GenerationSubmit(t.Context(), request)
		require.NoError(t, err)
		require.True(t, response.GetCreated())
		require.True(t, refused.Load())
	})

	// The submit contract against a running service. This stack's provider refuses every start, and
	// the submit settles a refused start at once: a resend of the failed request runs it again, rather
	// than serving the failure.
	t.Run("GenerationSubmit", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()

		created, err := client.GenerationSubmit(t.Context(), submitRequest(owner))
		require.NoError(t, err)
		require.True(t, created.GetCreated())
		require.Equal(t, servicegenai.GenerationStatusFailed, created.GetGeneration().GetStatus())
		require.Equal(t, servicegenai.GenerationFailureNotStarted, created.GetGeneration().GetFailureClass())
		require.NotNil(t, created.GetGeneration().GetSettledAt())

		// The caller decides to resend: the request runs from scratch, as another generation.
		rerun, err := client.GenerationSubmit(t.Context(), submitRequest(owner))
		require.NoError(t, err)
		require.True(t, rerun.GetCreated())
		require.NotEqual(t, created.GetGeneration().GetId(), rerun.GetGeneration().GetId())

		// Another variant of the same request is another generation.
		regenerate := submitRequest(owner)
		regenerate.Variant = 1
		variant, err := client.GenerationSubmit(t.Context(), regenerate)
		require.NoError(t, err)
		require.True(t, variant.GetCreated())

		// A purpose the service does not declare cannot be spent on: the embedded purposes reach the image.
		undeclared := submitRequest(owner)
		undeclared.Purpose = "studio.undeclared"
		_, err = client.GenerationSubmit(t.Context(), undeclared)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	// The largest payload the service accepts gets through the server's receive limit, and one byte more
	// is refused by the service's own validation, which names the limit, not by the transport.
	t.Run("LargestPayload", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()

		payloadOfSize := func(size int) []byte {
			head, tail := `{"model": "a-model", "input": "`, `"}`

			return []byte(head + strings.Repeat("x", size-len(head)-len(tail)) + tail)
		}

		largest := submitRequest(owner)
		largest.Payload = payloadOfSize(core.PayloadSizeCeiling)
		created, err := client.GenerationSubmit(t.Context(), largest)
		require.NoError(t, err)
		require.True(t, created.GetCreated())

		oversized := submitRequest(owner)
		oversized.Payload = payloadOfSize(core.PayloadSizeCeiling + 1)
		_, err = client.GenerationSubmit(t.Context(), oversized)
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

	// A cancel that comes after the generation settled, as every one does in this stack, succeeds with
	// the generation as it stands.
	t.Run("GenerationCancel", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()
		generation := submit(t, client, owner)

		cancelled, err := client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
			Id: generation.GetId(), OwnerId: owner,
		})
		require.NoError(t, err)
		require.Equal(t, servicegenai.GenerationStatusFailed, cancelled.GetGeneration().GetStatus())
		require.Equal(t, generation.GetSettledAt().AsTime(), cancelled.GetGeneration().GetSettledAt().AsTime())

		// A retry after a lost answer succeeds the same way.
		again, err := client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
			Id: generation.GetId(), OwnerId: owner,
		})
		require.NoError(t, err)
		require.Equal(t, servicegenai.GenerationStatusFailed, again.GetGeneration().GetStatus())

		// Another owner cannot stop it, and is told the same thing as if it did not exist.
		_, err = client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
			Id: generation.GetId(), OwnerId: uuid.Must(uuid.NewV7()).String(),
		})
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	// Over the caps a caller sends, the caller reads which limit refused, in admission's own words
	// and in the details' fields.
	t.Run("UsageCapReached", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()

		capsCases := []struct {
			caps *servicegenai.GenerationCaps

			expectMessage string
			expectLimit   string
		}{
			{
				// The count includes the generation the submit would create.
				caps: &servicegenai.GenerationCaps{Concurrency: new(int32(0))},

				expectMessage: `usage cap reached: purpose "studio.generation" concurrency: 0 generations already in flight`,
				expectLimit:   "concurrency 0",
			},
			{
				caps: &servicegenai.GenerationCaps{Windows: []*servicegenai.GenerationCapWindow{
					{Duration: durationpb.New(5 * time.Hour), InputTokens: new(int64(0))},
				}},

				expectMessage: `usage cap reached: purpose "studio.generation" over 5h0m0s`,
				expectLimit:   "input tokens over 5h",
			},
		}

		for _, capsCase := range capsCases {
			request := submitRequest(owner)
			request.Caps = capsCase.caps

			_, err := client.GenerationSubmit(t.Context(), request)
			require.Equal(t, codes.ResourceExhausted, status.Code(err))
			require.Equal(t, capsCase.expectMessage, status.Convert(err).Message())

			details := status.Convert(err).Details()
			require.Len(t, details, 1)

			quota, ok := details[0].(*errdetails.QuotaFailure)
			require.True(t, ok)
			require.Len(t, quota.GetViolations(), 1)
			require.Equal(t, `purpose "studio.generation"`, quota.GetViolations()[0].GetSubject())
			require.Equal(t, capsCase.expectLimit, quota.GetViolations()[0].GetDescription())
		}

		// The caller owns every limit: empty caps limit nothing.
		unlimited := submitRequest(owner)
		unlimited.Caps = &servicegenai.GenerationCaps{}

		created, err := client.GenerationSubmit(t.Context(), unlimited)
		require.NoError(t, err)
		require.True(t, created.GetCreated())
	})

	// The usage summary counts what the caps count, in the windows the caller names.
	t.Run("UsageSummary", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()

		summary, err := client.UsageSummary(t.Context(), &servicegenai.UsageSummaryRequest{
			OwnerId: owner, Purpose: "studio.generation",
			Windows: []*durationpb.Duration{durationpb.New(720 * time.Hour), durationpb.New(5 * time.Hour)},
		})
		require.NoError(t, err)
		require.Zero(t, summary.GetLive())
		require.Len(t, summary.GetWindows(), 2)
		require.Equal(t, 720*time.Hour, summary.GetWindows()[0].GetDuration().AsDuration())
		require.Equal(t, 5*time.Hour, summary.GetWindows()[1].GetDuration().AsDuration())
		require.Zero(t, summary.GetWindows()[0].GetInputTokens())

		// A purpose the service does not declare has no usage to report.
		_, err = client.UsageSummary(t.Context(), &servicegenai.UsageSummaryRequest{
			OwnerId: owner, Purpose: "studio.undeclared",
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	// The stream RPC end to end. This stack's provider refuses every start, so no call ever begins.
	t.Run("GenerationStream", func(t *testing.T) {
		t.Parallel()

		client := newClient(t)

		owner := uuid.Must(uuid.NewV7()).String()

		// A polled generation was not created to be streamed.
		polled := submit(t, client, owner)

		stream, err := client.GenerationStream(t.Context(), &servicegenai.GenerationStreamRequest{
			Id: polled.GetId(), OwnerId: owner,
		})
		require.NoError(t, err)

		_, err = stream.Recv()
		require.Equal(t, codes.FailedPrecondition, status.Code(err))

		// A streamed one whose call the provider never began has nothing to stream yet.
		request := submitRequest(owner)
		request.Stream = true

		streamed, err := client.GenerationSubmit(t.Context(), request)
		require.NoError(t, err)

		stream, err = client.GenerationStream(t.Context(), &servicegenai.GenerationStreamRequest{
			Id: streamed.GetGeneration().GetId(), OwnerId: owner,
		})
		require.NoError(t, err)

		_, err = stream.Recv()
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
	})
}
