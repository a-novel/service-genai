package handlers

import (
	"errors"
	"net/http"

	"google.golang.org/grpc"

	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/servicetoken"
)

// ErrServiceTokenIssuerRequired refuses a server configured with no service-token issuer: it would
// take every call.
var ErrServiceTokenIssuerRequired = errors.New("service token issuer required")

// GrpcServiceToken returns the server options that refuse a call unless its service token matches
// tokenConfig. The health service stays open for the kubelet's probes, and client fetches the
// issuer's keys.
//
// The gate fails closed: without an issuer it returns [ErrServiceTokenIssuerRequired], so a
// deployment that forgets the issuer stops at startup instead of admitting every caller. Only
// issuerOptional, which a local or test stack sets, turns that into no option, and the server then
// takes every call.
func GrpcServiceToken(
	tokenConfig servicetoken.Config, issuerOptional bool, client *http.Client,
) ([]grpc.ServerOption, error) {
	if tokenConfig.Issuer == "" {
		if issuerOptional {
			return nil, nil
		}

		return nil, ErrServiceTokenIssuerRequired
	}

	verifier := servicetoken.NewVerifier(tokenConfig, client)

	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(grpcf.ServiceTokenUnaryInterceptor(verifier)),
		grpc.ChainStreamInterceptor(grpcf.ServiceTokenStreamInterceptor(verifier)),
	}, nil
}
