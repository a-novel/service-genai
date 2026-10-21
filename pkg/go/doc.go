// Package servicegenai provides a gRPC client for the GenAI service.
//
// The service runs AI provider calls durably for other services. A caller builds the provider's own
// request, submits it, and polls the generation until it settles. Resending the same request after a
// crash returns the same generation, so the call is paid once. [NewClient] dials the service and
// returns a [Client] with one method per RPC.
package servicegenai
