// Package handlers is the transport layer of the GenAI service, sitting above the core layer.
// Handlers translate incoming gRPC requests into core calls and map the results and sentinel errors
// back to protocol responses and status codes; business logic lives in core.
//
// The gRPC API is private: its callers are other services on the internal network. It is wired by
// cmd/grpc.
package handlers
