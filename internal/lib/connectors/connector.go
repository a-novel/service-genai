package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"time"
)

// CallState is where a provider operation sits. It is the provider's own lifecycle, not the
// generation's: a call can finish while the generation around it is still being settled.
type CallState string

const (
	// CallQueued means the provider accepted the operation and has not begun it.
	CallQueued CallState = "queued"
	// CallRunning means the operation is in progress and must be polled again.
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
	return state != CallQueued && state != CallRunning
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
	// ErrStreamUnavailable means the provider no longer streams the operation, or never did: its
	// result is read with [Async.Get].
	ErrStreamUnavailable = errors.New("provider does not stream this operation")
	// ErrNotificationInvalid means a notification did not come from the provider.
	ErrNotificationInvalid = errors.New("notification failed verification")
	// ErrNotificationIgnored means a notification reports nothing this service acts on.
	ErrNotificationIgnored = errors.New("notification ignored")
	// ErrResponseTooLarge means the provider's answer is larger than the connector reads: the call
	// cannot be read, however often it is asked.
	ErrResponseTooLarge = errors.New("provider response too large")
)

// RejectionError is a start the provider refused outright. Nothing ran, so nothing was paid.
type RejectionError struct {
	// Response is the provider's error body, which tells the caller who built the request what to
	// fix. Empty when the provider sent none.
	Response json.RawMessage
	// Err is the refusal as the provider's client reported it.
	Err error
}

func (rejection *RejectionError) Error() string {
	return "provider rejected the request: " + rejection.Err.Error()
}

func (rejection *RejectionError) Unwrap() error {
	return rejection.Err
}

// Usage is what one operation consumed, as the provider counts it.
type Usage struct {
	// InputTokens counts every input token, cached ones included.
	InputTokens int64
	// CachedInputTokens are input tokens read from the provider's prompt cache.
	CachedInputTokens int64
	// CacheWriteTokens are input tokens written to the provider's prompt cache.
	CacheWriteTokens int64
	// OutputTokens counts every output token, reasoning ones included.
	OutputTokens int64
	// ReasoningTokens are output tokens the model spent reasoning.
	ReasoningTokens int64
}

// Call is one operation as the provider describes it.
type Call struct {
	// ID is the provider's own identifier. Recording it is what makes a generation resumable.
	ID string
	// State is where the operation sits in the provider's lifecycle.
	State CallState
	// Response is the provider's answer as it sent it, once the state is terminal.
	Response json.RawMessage
	// Usage is what the operation consumed, read off a terminal response. Absent when the provider
	// reports none, as for an operation that never reached the model.
	Usage *Usage
	// Model is the model snapshot that ran, read off a terminal response, which always carries one.
	Model string
	// Class is the reasoning effort that ran, read off a terminal response, which always carries one.
	// It is none when the call did not reason.
	Class string
	// Tier is the provider's service tier, which sets the unit price. Empty when the provider reports
	// none.
	Tier string
	// StartedAt is when the provider started the call, by its own clock. Zero when it reports none.
	StartedAt time.Time
	// CompletedAt is when the provider finished the call, by its own clock. Nil when it reports none.
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
	// Stream creates the operation in streaming mode, so [Streaming] can read its events while it
	// runs.
	Stream bool
	// Accepted, when set, receives the operation the moment the provider names it, when that comes
	// before Start returns, as for a streamed start held until the provider begins it. Its error ends
	// the start, and Start returns it.
	Accepted func(call *Call) error
}

// An Async runs calls in the Async mode: the provider accepts a call and runs it on its side,
// and the service reads it back by id. Three operations are all a check needs, which is what lets
// any provider with an asynchronous API offer the mode.
type Async interface {
	// Start begins an operation and returns as soon as the provider accepts it, without waiting for
	// the model. The operation's ID must be recorded before anything else happens, through
	// [AsyncStartRequest.Accepted] when the provider names it before Start returns. An
	// [ErrStartAmbiguous] outcome cannot safely be retried as a new operation.
	Start(ctx context.Context, request *AsyncStartRequest) (*Call, error)
	// Get reads an operation by id. It is both the poll and the re-attach.
	Get(ctx context.Context, id string) (*Call, error)
	// Cancel stops an operation. Cancelling a finished operation returns its final state rather than
	// failing.
	Cancel(ctx context.Context, id string) (*Call, error)
}

// A Streaming reads the events of an operation started with [AsyncStartRequest.Stream], as the
// provider produces them.
type Streaming interface {
	// Stream yields the operation's events after the cursor, or from its first one when there is
	// none, and stops after a terminal event. [ErrStreamUnavailable] means the provider no longer
	// streams the operation.
	Stream(ctx context.Context, request *StreamRequest) iter.Seq2[*StreamEvent, error]
}

// StreamRequest is the input to [Streaming.Stream].
type StreamRequest struct {
	// ID identifies the operation.
	ID string
	// StartingAfter is the cursor: the sequence number of the last event already received.
	StartingAfter *int64
}

// StreamEvent is one event of an operation, as the provider sent it.
type StreamEvent struct {
	// Sequence orders the operation's events; a stream resumes after it.
	Sequence int64
	// Data is the event as sent.
	Data json.RawMessage
	// Terminal marks the operation's last event.
	Terminal bool
}

// A Notifications reads the provider's notifications that its operations ended, so the service
// checks them without waiting for a poll. Verifying them is the connector's: only a notification
// that comes from the provider names an operation.
type Notifications interface {
	// Notification verifies a notification and names the operation it concerns. [ErrModeUnsupported]
	// means the connector has no means to verify one.
	Notification(ctx context.Context, request *NotificationRequest) (*Notification, error)
}

// NotificationRequest is a notification as the provider sent it.
type NotificationRequest struct {
	// Body is the notification's body, as received: a signature covers its exact bytes.
	Body []byte
	// Header holds the notification's headers, which carry its signature.
	Header map[string][]string
}

// Notification names the operation a notification concerns.
type Notification struct {
	// CallID identifies the operation that ended.
	CallID string
}

// Registry names the connector serving each provider. A connector offers a mode by implementing the
// mode's interface, so what a provider supports is decided by its code and cannot be misdeclared.
type Registry map[string]any

// NewRegistry builds the connectors the service runs, named by the provider each serves. Every
// entrypoint and the conformance check build them here, so what the check proves is what runs.
func NewRegistry(openAI OpenAIConfig) (Registry, error) {
	connector, err := NewOpenAI(openAI)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}

	return Registry{"openai": connector}, nil
}

// Streaming returns the connector serving the provider in the Streaming mode.
func (registry Registry) Streaming(provider string) (Streaming, error) {
	return modeOf[Streaming](registry, provider)
}

// Notifications returns the connector reading the provider's notifications.
func (registry Registry) Notifications(provider string) (Notifications, error) {
	return modeOf[Notifications](registry, provider)
}

// Async returns the connector running the provider's calls in the Async mode.
func (registry Registry) Async(provider string) (Async, error) {
	return modeOf[Async](registry, provider)
}

// modeOf returns the provider's connector in the mode M, which it offers by implementing M.
func modeOf[M any](registry Registry, provider string) (M, error) {
	var mode M

	connector, ok := registry[provider]
	if !ok {
		return mode, ErrProviderUnknown
	}

	mode, ok = connector.(M)
	if !ok {
		return mode, ErrModeUnsupported
	}

	return mode, nil
}
