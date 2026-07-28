package servicegenai

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"

	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// Request, response, and entity types are re-exported from the service's generated
// protobuf definitions, so callers never import the service's internal packages.
type (
	StatusRequest  = genaiv0.StatusRequest
	StatusResponse = genaiv0.StatusResponse
	QueueDepth     = genaiv0.QueueDepth

	GenerationSubmitRequest  = genaiv0.GenerationSubmitRequest
	GenerationSubmitResponse = genaiv0.GenerationSubmitResponse
	GenerationGetRequest     = genaiv0.GenerationGetRequest
	GenerationGetResponse    = genaiv0.GenerationGetResponse
	GenerationCancelRequest  = genaiv0.GenerationCancelRequest
	GenerationCancelResponse = genaiv0.GenerationCancelResponse
	GenerationWatchRequest   = genaiv0.GenerationWatchRequest
	GenerationWatchResponse  = genaiv0.GenerationWatchResponse

	UsageQueryRequest  = genaiv0.UsageQueryRequest
	UsageQueryResponse = genaiv0.UsageQueryResponse
	UsageGroup         = genaiv0.UsageGroup
	UsageTotal         = genaiv0.UsageTotal

	Generation       = genaiv0.Generation
	GenerationStatus = genaiv0.GenerationStatus
)

// Terminal statuses, re-exported so a caller can decide whether to keep waiting without importing
// the generated package.
const (
	GenerationStatusPending   = genaiv0.GenerationStatus_GENERATION_STATUS_PENDING
	GenerationStatusRunning   = genaiv0.GenerationStatus_GENERATION_STATUS_RUNNING
	GenerationStatusSucceeded = genaiv0.GenerationStatus_GENERATION_STATUS_SUCCEEDED
	GenerationStatusFailed    = genaiv0.GenerationStatus_GENERATION_STATUS_FAILED
	GenerationStatusAbandoned = genaiv0.GenerationStatus_GENERATION_STATUS_ABANDONED
	GenerationStatusCancelled = genaiv0.GenerationStatus_GENERATION_STATUS_CANCELLED
)

// A Client issues the service's gRPC calls, one method per RPC. Construct one
// with [NewClient] and call Close when finished to release the connection.
type Client interface {
	UnaryEcho(
		ctx context.Context, req *golibproto.UnaryEchoRequest, opts ...grpc.CallOption,
	) (*golibproto.UnaryEchoResponse, error)
	Status(ctx context.Context, req *StatusRequest, opts ...grpc.CallOption) (*StatusResponse, error)

	// GenerationSubmit records a generation. The idempotency key is required: a replay attaches to
	// the work already in flight rather than paying for a second run, and the response reports
	// which happened.
	GenerationSubmit(
		ctx context.Context, req *GenerationSubmitRequest, opts ...grpc.CallOption,
	) (*GenerationSubmitResponse, error)
	// GenerationGet reads one of an owner's generations. Another owner's reports not-found.
	GenerationGet(
		ctx context.Context, req *GenerationGetRequest, opts ...grpc.CallOption,
	) (*GenerationGetResponse, error)
	// GenerationCancel stops a generation so an abandoned one stops costing.
	GenerationCancel(
		ctx context.Context, req *GenerationCancelRequest, opts ...grpc.CallOption,
	) (*GenerationCancelResponse, error)
	// GenerationWatch streams a generation's state until it is terminal. Resumable: a caller that
	// reconnects calls it again and is answered from current state.
	GenerationWatch(
		ctx context.Context, req *GenerationWatchRequest, opts ...grpc.CallOption,
	) (grpc.ServerStreamingClient[GenerationWatchResponse], error)
	// UsageQuery reports what an owner consumed over a window, grouped by purpose and model. It
	// returns tokens, never money: what a token costs is the caller's decision, which is why the
	// model that actually billed is on every group.
	UsageQuery(
		ctx context.Context, req *UsageQueryRequest, opts ...grpc.CallOption,
	) (*UsageQueryResponse, error)

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
	genaiv0.GenerationWatchServiceClient
	genaiv0.UsageQueryServiceClient

	conn *grpc.ClientConn
}

func (c *client) Close() {
	_ = c.conn.Close()
}

// NewClient creates a [Client] for the service reachable at addr. The
// connection is established lazily on the first RPC. Dial options are forwarded
// to the underlying gRPC connection.
func NewClient(addr string, opts ...grpc.DialOption) (Client, error) {
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
		GenerationWatchServiceClient:  genaiv0.NewGenerationWatchServiceClient(conn),
		UsageQueryServiceClient:       genaiv0.NewUsageQueryServiceClient(conn),
		conn:                          conn,
	}, nil
}
