package servicegenai

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"

	"github.com/a-novel/service-genai/internal/handlers/protogen"
	genaiv1 "github.com/a-novel/service-genai/internal/handlers/protogen/genai/v1"
)

// Request, response, and entity types are re-exported from the service's generated
// protobuf definitions, so callers never import the service's internal packages.
type (
	StatusRequest  = genaiv1.StatusRequest
	StatusResponse = genaiv1.StatusResponse
	QueueDepth     = genaiv1.QueueDepth

	GenerationSubmitRequest  = protogen.GenerationSubmitRequest
	GenerationSubmitResponse = protogen.GenerationSubmitResponse
	GenerationGetRequest     = protogen.GenerationGetRequest
	GenerationGetResponse    = protogen.GenerationGetResponse
	GenerationCancelRequest  = protogen.GenerationCancelRequest
	GenerationCancelResponse = protogen.GenerationCancelResponse
	GenerationWatchRequest   = protogen.GenerationWatchRequest
	GenerationWatchResponse  = protogen.GenerationWatchResponse

	UsageQueryRequest  = protogen.UsageQueryRequest
	UsageQueryResponse = protogen.UsageQueryResponse
	UsageGroup         = protogen.UsageGroup
	UsageTotal         = protogen.UsageTotal

	Generation       = protogen.Generation
	GenerationStatus = protogen.GenerationStatus
)

const legacyStatusFullMethodName = "/StatusService/Status"

// Terminal statuses, re-exported so a caller can decide whether to keep waiting without importing
// the generated package.
const (
	GenerationStatusPending   = protogen.GenerationStatus_GENERATION_STATUS_PENDING
	GenerationStatusRunning   = protogen.GenerationStatus_GENERATION_STATUS_RUNNING
	GenerationStatusSucceeded = protogen.GenerationStatus_GENERATION_STATUS_SUCCEEDED
	GenerationStatusFailed    = protogen.GenerationStatus_GENERATION_STATUS_FAILED
	GenerationStatusAbandoned = protogen.GenerationStatus_GENERATION_STATUS_ABANDONED
	GenerationStatusCancelled = protogen.GenerationStatus_GENERATION_STATUS_CANCELLED
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
	protogen.GenerationSubmitServiceClient
	protogen.GenerationGetServiceClient
	protogen.GenerationCancelServiceClient
	protogen.GenerationWatchServiceClient
	protogen.UsageQueryServiceClient

	conn *grpc.ClientConn
}

type legacyStatusServiceClient struct {
	cc grpc.ClientConnInterface
}

func (client *legacyStatusServiceClient) Status(
	ctx context.Context,
	request *StatusRequest,
	opts ...grpc.CallOption,
) (*StatusResponse, error) {
	callOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)

	response := new(StatusResponse)

	err := client.cc.Invoke(ctx, legacyStatusFullMethodName, request, response, callOpts...)
	if err != nil {
		return nil, err
	}

	return response, nil
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
		StatusServiceClient:           &legacyStatusServiceClient{cc: conn},
		GenerationSubmitServiceClient: protogen.NewGenerationSubmitServiceClient(conn),
		GenerationGetServiceClient:    protogen.NewGenerationGetServiceClient(conn),
		GenerationCancelServiceClient: protogen.NewGenerationCancelServiceClient(conn),
		GenerationWatchServiceClient:  protogen.NewGenerationWatchServiceClient(conn),
		UsageQueryServiceClient:       protogen.NewUsageQueryServiceClient(conn),
		conn:                          conn,
	}, nil
}
