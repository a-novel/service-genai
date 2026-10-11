package servicegenai

import (
	"context"
	"fmt"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"

	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// Request, response, and entity types are re-exported from the service's generated
// protobuf definitions, so callers never import the service's internal packages.
type (
	StatusRequest    = genaiv0.StatusRequest
	StatusResponse   = genaiv0.StatusResponse
	QueueDepth       = genaiv0.QueueDepth
	DependencyHealth = genaiv0.DependencyHealth
	DependencyStatus = genaiv0.DependencyStatus

	GenerationSubmitRequest  = genaiv0.GenerationSubmitRequest
	GenerationSubmitResponse = genaiv0.GenerationSubmitResponse
	GenerationGetRequest     = genaiv0.GenerationGetRequest
	GenerationGetResponse    = genaiv0.GenerationGetResponse
	GenerationCancelRequest  = genaiv0.GenerationCancelRequest
	GenerationCancelResponse = genaiv0.GenerationCancelResponse

	Generation        = genaiv0.Generation
	GenerationStatus  = genaiv0.GenerationStatus
	GenerationAttempt = genaiv0.GenerationAttempt
)

// Dependency statuses a Status report carries.
const (
	DependencyStatusUnspecified = genaiv0.DependencyStatus_DEPENDENCY_STATUS_UNSPECIFIED
	DependencyStatusUp          = genaiv0.DependencyStatus_DEPENDENCY_STATUS_UP
	DependencyStatusDown        = genaiv0.DependencyStatus_DEPENDENCY_STATUS_DOWN
)

// Generation statuses, re-exported so a caller can decide whether to keep waiting without importing
// the generated package. Succeeded, failed and cancelled are terminal.
const (
	GenerationStatusUnspecified = genaiv0.GenerationStatus_GENERATION_STATUS_UNSPECIFIED
	GenerationStatusPending     = genaiv0.GenerationStatus_GENERATION_STATUS_PENDING
	GenerationStatusRunning     = genaiv0.GenerationStatus_GENERATION_STATUS_RUNNING
	GenerationStatusSucceeded   = genaiv0.GenerationStatus_GENERATION_STATUS_SUCCEEDED
	GenerationStatusFailed      = genaiv0.GenerationStatus_GENERATION_STATUS_FAILED
	GenerationStatusCancelled   = genaiv0.GenerationStatus_GENERATION_STATUS_CANCELLED
)

// maxReceiveBytes lets a generation through whole: its attempts carry the provider's raw responses,
// and several of them can outgrow gRPC's default 4 MiB receive limit.
const maxReceiveBytes = 64 << 20

// A Client issues the service's gRPC calls, one method per RPC. Construct one
// with [NewClient] and call Close when finished to release the connection.
type Client interface {
	UnaryEcho(
		ctx context.Context, req *golibproto.UnaryEchoRequest, opts ...grpc.CallOption,
	) (*golibproto.UnaryEchoResponse, error)
	// Status verifies PostgreSQL and queue inspection, returning Unavailable if either fails.
	// A successful response includes both reports. Use UnaryEcho for process-only liveness.
	// Callers should set a deadline on ctx.
	Status(ctx context.Context, req *StatusRequest, opts ...grpc.CallOption) (*StatusResponse, error)

	// GenerationSubmit records a generation and starts it in the Async mode: the payload is the
	// provider's own request, which the service forwards without reading. Resending the same request
	// is a replay: it returns the pending, running or succeeded generation rather than paying again,
	// and the response reports which happened. A request whose generation failed or was cancelled
	// runs again.
	GenerationSubmit(
		ctx context.Context, req *GenerationSubmitRequest, opts ...grpc.CallOption,
	) (*GenerationSubmitResponse, error)
	// GenerationGet reads one of an owner's generations, checking it with the provider when it is
	// stale. Poll it until the generation settles. Another owner's reports not-found.
	GenerationGet(
		ctx context.Context, req *GenerationGetRequest, opts ...grpc.CallOption,
	) (*GenerationGetResponse, error)
	// GenerationCancel stops a generation so an abandoned one stops costing. A generation already
	// settled comes back as it stands, so a retried cancel succeeds; read its status to know which.
	GenerationCancel(
		ctx context.Context, req *GenerationCancelRequest, opts ...grpc.CallOption,
	) (*GenerationCancelResponse, error)

	// Close releases the underlying gRPC connection. Call it once the client is
	// no longer needed.
	Close()
}

type client struct {
	golibproto.EchoServiceClient
	genaiv0.StatusServiceClient
	genaiv0.GenerationSubmitServiceClient
	genaiv0.GenerationGetServiceClient
	genaiv0.GenerationCancelServiceClient

	conn *grpc.ClientConn
}

func (c *client) Close() {
	_ = c.conn.Close()
}

// NewClient creates a [Client] for the service reachable at addr. The
// connection is established lazily on the first RPC. Dial options are forwarded
// to the underlying gRPC connection, after a receive limit large enough for a
// generation's responses.
//
// Each call opens a client span and sends the caller's trace context, so the server's
// spans join the caller's trace.
func NewClient(addr string, opts ...grpc.DialOption) (Client, error) {
	opts = append([]grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxReceiveBytes)),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	}, opts...)

	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("new grpc client: %w", err)
	}

	return &client{
		EchoServiceClient:             golibproto.NewEchoServiceClient(conn),
		StatusServiceClient:           genaiv0.NewStatusServiceClient(conn),
		GenerationSubmitServiceClient: genaiv0.NewGenerationSubmitServiceClient(conn),
		GenerationGetServiceClient:    genaiv0.NewGenerationGetServiceClient(conn),
		GenerationCancelServiceClient: genaiv0.NewGenerationCancelServiceClient(conn),
		conn:                          conn,
	}, nil
}
