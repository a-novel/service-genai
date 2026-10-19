// Package handlers is the transport layer of the GenAI service, sitting above the core layer.
// Handlers translate incoming requests into core calls and map the results and sentinel errors back
// to protocol responses and status codes; business logic lives in core.
//
// Two transport implementations serve distinct audiences:
//
//   - gRPC handlers: the private generation API, for other services on the internal network.
//     Wired by cmd/grpc.
//
//   - REST handlers: the public endpoint providers post their notifications to, wired by cmd/rest,
//     and the private sweep a scheduler calls, wired by cmd/sweep.
package handlers
