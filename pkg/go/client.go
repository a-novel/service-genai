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
	TierListRequest          = genaiv0.TierListRequest
	TierListResponse         = genaiv0.TierListResponse
	TierCeilings             = genaiv0.TierCeilings

	Generation        = genaiv0.Generation
	GenerationStatus  = genaiv0.GenerationStatus
	GenerationFailure = genaiv0.GenerationFailure
	GenerationUsage   = genaiv0.GenerationUsage
	Tier              = genaiv0.Tier
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

// Failure kinds a failed generation carries, re-exported so a caller can act on them.
const (
	GenerationFailureUnspecified    = genaiv0.GenerationFailure_GENERATION_FAILURE_UNSPECIFIED
	GenerationFailureRefused        = genaiv0.GenerationFailure_GENERATION_FAILURE_REFUSED
	GenerationFailureIncomplete     = genaiv0.GenerationFailure_GENERATION_FAILURE_INCOMPLETE
	GenerationFailureInvalidRequest = genaiv0.GenerationFailure_GENERATION_FAILURE_INVALID_REQUEST
	GenerationFailureFailed         = genaiv0.GenerationFailure_GENERATION_FAILURE_FAILED
)

// Tiers a caller picks from, re-exported so a caller never imports the generated package.
const (
	TierUnspecified = genaiv0.Tier_TIER_UNSPECIFIED
	TierFast        = genaiv0.Tier_TIER_FAST
	TierBalanced    = genaiv0.Tier_TIER_BALANCED
	TierDeep        = genaiv0.Tier_TIER_DEEP
)

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

	// GenerationSubmit records a generation and starts it. Resending the same request is a replay: it
	// returns the pending, running or succeeded generation rather than paying again, and the response
	// reports which happened. A request whose generation failed or was cancelled runs again.
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

	// TierList reports every Tier's input and output token ceilings, from the fastest to the most
	// capable, so a caller sizes its input and its schema before submitting. The input ceiling is
	// advertised, not enforced: an input past it fails the generation as INVALID_REQUEST.
	TierList(ctx context.Context, req *TierListRequest, opts ...grpc.CallOption) (*TierListResponse, error)

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
	genaiv0.TierListServiceClient

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
		TierListServiceClient:         genaiv0.NewTierListServiceClient(conn),
		conn:                          conn,
	}, nil
}
