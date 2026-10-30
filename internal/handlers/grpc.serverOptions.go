package handlers

import (
	"google.golang.org/grpc"

	"github.com/a-novel-kit/golib/grpcf"
)

// GrpcServerOptions returns the server options that admit a submit at its payload ceiling, refuse a
// call past callLimit so the requests in memory stay bounded, and make a stopping server wait for its
// handlers, so none still runs once the database closes.
func GrpcServerOptions(callLimit *grpcf.CallLimit) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(GrpcMaxReceiveBytes),
		callLimit,
		grpc.WaitForHandlers(true),
	}
}
