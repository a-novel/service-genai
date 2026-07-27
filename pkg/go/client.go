package servicegenai

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"

	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

// Request, response, and entity types are re-exported from the service's generated
// protobuf definitions, so callers never import the service's internal packages.
type (
	StatusRequest  = protogen.StatusRequest
	StatusResponse = protogen.StatusResponse
)

// A Client issues the service's gRPC calls, one method per RPC. Construct one
// with [NewClient] and call Close when finished to release the connection.
type Client interface {
	UnaryEcho(
		ctx context.Context, req *golibproto.UnaryEchoRequest, opts ...grpc.CallOption,
	) (*golibproto.UnaryEchoResponse, error)
	Status(ctx context.Context, req *StatusRequest, opts ...grpc.CallOption) (*StatusResponse, error)

	// Close releases the underlying gRPC connection. Call it once the client is
	// no longer needed.
	Close()
}

type client struct {
	golibproto.EchoServiceClient
	protogen.StatusServiceClient

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
		EchoServiceClient:   golibproto.NewEchoServiceClient(conn),
		StatusServiceClient: protogen.NewStatusServiceClient(conn),
		conn:                conn,
	}, nil
}
