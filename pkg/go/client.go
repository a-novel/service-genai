package servicegenai

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"

	genaiv1 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v1"
)

// Request, response, and entity types are re-exported from the service's generated
// protobuf definitions, so callers never import the service's internal packages.
type (
	StatusRequest  = genaiv1.StatusRequest
	StatusResponse = genaiv1.StatusResponse
	QueueDepth     = genaiv1.QueueDepth

	GenerationSubmitRequest  = genaiv1.GenerationSubmitRequest
	GenerationSubmitResponse = genaiv1.GenerationSubmitResponse
	GenerationGetRequest     = genaiv1.GenerationGetRequest
	GenerationGetResponse    = genaiv1.GenerationGetResponse
	GenerationCancelRequest  = genaiv1.GenerationCancelRequest
	GenerationCancelResponse = genaiv1.GenerationCancelResponse
	GenerationWatchRequest   = genaiv1.GenerationWatchRequest
	GenerationWatchResponse  = genaiv1.GenerationWatchResponse

	UsageQueryRequest  = genaiv1.UsageQueryRequest
	UsageQueryResponse = genaiv1.UsageQueryResponse
	UsageGroup         = genaiv1.UsageGroup
	UsageTotal         = genaiv1.UsageTotal

	Generation       = genaiv1.Generation
	GenerationStatus = genaiv1.GenerationStatus
)

// Terminal statuses, re-exported so a caller can decide whether to keep waiting without importing
// the generated package.
const (
	GenerationStatusPending   = genaiv1.GenerationStatus_GENERATION_STATUS_PENDING
	GenerationStatusRunning   = genaiv1.GenerationStatus_GENERATION_STATUS_RUNNING
	GenerationStatusSucceeded = genaiv1.GenerationStatus_GENERATION_STATUS_SUCCEEDED
	GenerationStatusFailed    = genaiv1.GenerationStatus_GENERATION_STATUS_FAILED
	GenerationStatusAbandoned = genaiv1.GenerationStatus_GENERATION_STATUS_ABANDONED
	GenerationStatusCancelled = genaiv1.GenerationStatus_GENERATION_STATUS_CANCELLED
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
	genaiv1.StatusServiceClient
	genaiv1.GenerationSubmitServiceClient
	genaiv1.GenerationGetServiceClient
	genaiv1.GenerationCancelServiceClient
	genaiv1.GenerationWatchServiceClient
	genaiv1.UsageQueryServiceClient

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
		StatusServiceClient:           genaiv1.NewStatusServiceClient(conn),
		GenerationSubmitServiceClient: genaiv1.NewGenerationSubmitServiceClient(conn),
		GenerationGetServiceClient:    genaiv1.NewGenerationGetServiceClient(conn),
		GenerationCancelServiceClient: genaiv1.NewGenerationCancelServiceClient(conn),
		GenerationWatchServiceClient:  genaiv1.NewGenerationWatchServiceClient(conn),
		UsageQueryServiceClient:       genaiv1.NewUsageQueryServiceClient(conn),
		conn:                          conn,
	}, nil
}
