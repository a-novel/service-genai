package handlers

import (
	"google.golang.org/grpc"

	"github.com/a-novel-kit/golib/grpcf"
)

// GrpcServerOptions returns the server options that admit a submit at its payload ceiling, refuse a
// call past maxConcurrentCalls so the requests in memory stay bounded, and make a stopping server
// wait for its handlers, so none still runs once the database closes.
func GrpcServerOptions(maxConcurrentCalls int) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(GrpcMaxReceiveBytes),
		grpcf.MaxConcurrentCalls(maxConcurrentCalls),
		grpc.WaitForHandlers(true),
	}
}
