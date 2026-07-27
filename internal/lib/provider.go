// Package lib holds the provider adapter: the one place in the platform that talks to a generative
// AI provider.
package lib

import (
	"context"
	"encoding/json"
	"errors"
)

// ProviderCallState is where a provider operation sits. It is the provider's own lifecycle, not the
// generation's — a call can finish while the generation around it is still being settled.
type ProviderCallState string

const (
	// ProviderCallRunning means the operation is queued or in progress and must be polled again.
	ProviderCallRunning ProviderCallState = "running"
	// ProviderCallSucceeded means the operation produced a usable output.
	ProviderCallSucceeded ProviderCallState = "succeeded"
	// ProviderCallIncomplete means the operation stopped early — an output cap, or a refusal. It is
	// terminal and it consumed tokens.
	ProviderCallIncomplete ProviderCallState = "incomplete"
	// ProviderCallFailed means the operation failed. Terminal; it may or may not have consumed
	// tokens.
	ProviderCallFailed ProviderCallState = "failed"
	// ProviderCallCancelled means the operation was cancelled.
	ProviderCallCancelled ProviderCallState = "cancelled"
)

// Terminal reports whether the state needs no further polling.
func (state ProviderCallState) Terminal() bool {
	return state != ProviderCallRunning
}

// ErrProviderRetryable marks a failure worth another attempt: a transport error, a rate limit, or a
// provider-side fault. Everything else is terminal, because retrying a rejected request only spends
// another attempt to be rejected again.
var ErrProviderRetryable = errors.New("retryable provider failure")

// ProviderUsage is what the provider reported consuming. The totals include the detail counts, as
// the provider counts them.
type ProviderUsage struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ReasoningTokens   int64
}

// ProviderCall is one operation as the provider describes it.
type ProviderCall struct {
	// ID is the provider's own identifier. Recording it is what makes a generation resumable.
	ID    string
	State ProviderCallState
	// Model that actually ran. Read off the response, since a provider may serve a different
	// snapshot than the one asked for.
	Model string
	// Output is the provider's response, verbatim. Opaque: the caller composed the request and owns
	// the shape of what comes back.
	Output json.RawMessage
	// Usage is absent while running, and on a failure that never reached the model.
	Usage *ProviderUsage
	// Reason explains a non-successful terminal state — the incomplete reason, or the error.
	Reason string
}

// ProviderStartRequest is the input to [Provider.Start].
type ProviderStartRequest struct {
	// Request is the caller's provider payload, forwarded verbatim.
	Request json.RawMessage
	// GenerationID and Attempt are stamped on the provider side so an operation orphaned by a crash
	// stays identifiable.
	GenerationID string
	Attempt      int16
}

// Provider is the narrow surface a worker needs. Keeping it to four operations is what leaves room
// for a second provider without rewriting the worker.
type Provider interface {
	// Start begins an operation and returns as soon as the provider accepts it, without waiting for
	// the model. The returned ID must be recorded before anything else happens.
	Start(ctx context.Context, request *ProviderStartRequest) (*ProviderCall, error)
	// Get reads an operation by id. It is both the poll and the re-attach: a generation recovered
	// from a crash calls it and resumes the operation already paid for.
	Get(ctx context.Context, id string) (*ProviderCall, error)
	// Cancel stops an operation. Idempotent — cancelling a terminal operation returns its final
	// state rather than failing.
	Cancel(ctx context.Context, id string) (*ProviderCall, error)
	// Name identifies the provider on the usage record.
	Name() string
}
