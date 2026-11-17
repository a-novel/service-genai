// Package handlers is the transport layer of the GenAI service, sitting above the core layer.
// Handlers translate incoming requests into core calls and map the results and sentinel errors back
// to protocol responses and status codes; business logic lives in core.
//
// Its gRPC handlers serve the private generation API to other services on the internal network.
// cmd/grpc wires them behind golib's service-token gate, which admits only the callers whose service
// token it accepts. The sweep server's liveness and health probes are golib's own handlers.
package handlers
