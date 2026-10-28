package handlers

import (
	"net/http"

	"google.golang.org/grpc"

	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/servicetoken"
)

// GrpcServiceToken returns the server options that refuse a call unless its service token matches
// tokenConfig. The health service stays open for the kubelet's probes, and client fetches the
// issuer's keys. Without an issuer, it returns no option, and the server takes every call.
func GrpcServiceToken(tokenConfig servicetoken.Config, client *http.Client) []grpc.ServerOption {
	if tokenConfig.Issuer == "" {
		return nil
	}

	verifier := servicetoken.NewVerifier(tokenConfig, client)

	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(grpcf.ServiceTokenUnaryInterceptor(verifier)),
		grpc.ChainStreamInterceptor(grpcf.ServiceTokenStreamInterceptor(verifier)),
	}
}
