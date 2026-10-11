package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// CallState is where a provider operation sits. It is the provider's own lifecycle, not the
// generation's: a call can finish while the generation around it is still being settled.
type CallState string

const (
	// CallRunning means the operation is queued or in progress and must be polled again.
	CallRunning CallState = "running"
	// CallSucceeded means the provider finished the operation. Whether its output is usable
	// is the caller's reading of the response.
	CallSucceeded CallState = "succeeded"
	// CallFailed means the provider reports the operation failed. Terminal; it may have
	// consumed tokens.
	CallFailed CallState = "failed"
	// CallCancelled means the operation was cancelled.
	CallCancelled CallState = "cancelled"
)

// Terminal reports whether the state needs no further polling.
func (state CallState) Terminal() bool {
	return state != CallRunning
}

var (
	// ErrRetryable marks a provider interaction that can be retried within its current
	// lifecycle step: nothing was accepted, or the operation can be read again.
	ErrRetryable = errors.New("retryable provider failure")
	// ErrStartAmbiguous means the provider may have accepted paid work without returning its
	// ID.
	ErrStartAmbiguous = errors.New("provider start outcome is ambiguous")
	// ErrProviderUnknown is returned for a provider no connector serves.
	ErrProviderUnknown = errors.New("unknown provider")
	// ErrModeUnsupported is returned for a provider whose connector does not offer the mode.
	ErrModeUnsupported = errors.New("provider does not support this mode")
)

// RejectionError is a start the provider refused outright. Nothing ran, so nothing was paid.
type RejectionError struct {
	// Response is the provider's error body, which tells the caller who built the request what to
	// fix. Empty when the provider sent none.
	Response json.RawMessage
	Err      error
}

func (rejection *RejectionError) Error() string {
	return "provider rejected the request: " + rejection.Err.Error()
}

func (rejection *RejectionError) Unwrap() error {
	return rejection.Err
}

// Usage is what one operation consumed, as the provider counts it: input includes cached
// input, and output includes reasoning.
type Usage struct {
	InputTokens       int64
	CachedInputTokens int64
	// CacheWriteTokens are input tokens written to the provider's prompt cache.
	CacheWriteTokens int64
	OutputTokens     int64
	ReasoningTokens  int64
}

// Call is one operation as the provider describes it.
type Call struct {
	// ID is the provider's own identifier. Recording it is what makes a generation resumable.
	ID    string
	State CallState
	// Response is the provider's answer as it sent it, once the state is terminal.
	Response json.RawMessage
	// Usage is what the operation consumed, read off a terminal response. Absent when the provider
	// reports none, as for an operation that never reached the model.
	Usage *Usage
	// Model, Class and Tier are what actually ran, read off a terminal response: the model snapshot,
	// the reasoning effort (none when the call did not reason), and the provider's service tier,
	// which sets the unit price. A terminal call always carries a Model and a Class; Tier is empty
	// when the provider reports none.
	Model string
	Class string
	Tier  string
	// StartedAt and CompletedAt are when the provider started and finished the call, by its own
	// clock. StartedAt is zero, and CompletedAt nil, when the provider reports none.
	StartedAt   time.Time
	CompletedAt *time.Time
	// Reason is the provider's own account of a failure, for server-side logs only.
	Reason string
	// Retryable permits a fresh attempt after this failure is recorded.
	Retryable bool
}

// AsyncStartRequest is the input to [Async.Start].
type AsyncStartRequest struct {
	// Payload is the provider's own request, built by the caller. The connector writes the fields the
	// Async mode needs over it and reads nothing else.
	Payload json.RawMessage
}

// An Async runs calls in the Async mode: the provider accepts a call and runs it on its side,
// and the service reads it back by id. Three operations are all a check needs, which is what lets
// any provider with an asynchronous API offer the mode.
type Async interface {
	// Start begins an operation and returns as soon as the provider accepts it, without waiting for
	// the model. The returned ID must be recorded before anything else happens. An
	// [ErrStartAmbiguous] outcome cannot safely be retried as a new operation.
	Start(ctx context.Context, request *AsyncStartRequest) (*Call, error)
	// Get reads an operation by id. It is both the poll and the re-attach.
	Get(ctx context.Context, id string) (*Call, error)
	// Cancel stops an operation. Cancelling a finished operation returns its final state rather than
	// failing.
	Cancel(ctx context.Context, id string) (*Call, error)
}

// Registry names the connector serving each provider. A connector offers a mode by implementing the
// mode's interface, so what a provider supports is decided by its code and cannot be misdeclared.
type Registry map[string]any

// Async returns the connector running the provider's calls in the Async mode.
func (registry Registry) Async(provider string) (Async, error) {
	connector, ok := registry[provider]
	if !ok {
		return nil, ErrProviderUnknown
	}

	async, ok := connector.(Async)
	if !ok {
		return nil, ErrModeUnsupported
	}

	return async, nil
}
