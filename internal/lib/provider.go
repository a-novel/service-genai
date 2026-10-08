// Package lib holds the provider adapter: the one place in the platform that talks to a generative
// AI provider.
package lib

import (
	"context"
	"encoding/json"
	"errors"
)

// Tier is a level of model capability. Callers pick one; each provider binds it to a model.
type Tier string

const (
	TierFast     Tier = "fast"
	TierBalanced Tier = "balanced"
	TierDeep     Tier = "deep"
)

// Tiers lists every Tier, so a provider can be checked for a binding of each.
var Tiers = []Tier{TierFast, TierBalanced, TierDeep}

// TierBinding is what a Tier runs on one provider.
type TierBinding struct {
	Model string
	// ReasoningEffort is passed to the provider as is: effort values are not portable across providers.
	// Empty sends none.
	ReasoningEffort string
	// MaxOutputTokens caps the output, reasoning included, and with it the spend of one call.
	MaxOutputTokens int64
}

// ProviderCallState is where a provider operation sits. It is the provider's own lifecycle, not the
// generation's — a call can finish while the generation around it is still being settled.
type ProviderCallState string

const (
	// ProviderCallRunning means the operation is queued or in progress and must be polled again.
	ProviderCallRunning ProviderCallState = "running"
	// ProviderCallSucceeded means the operation produced an output conforming to the schema.
	ProviderCallSucceeded ProviderCallState = "succeeded"
	// ProviderCallFailed means the operation ended without a usable output: refused, incomplete or
	// failed. Terminal; it may have consumed tokens. Failure says how.
	ProviderCallFailed ProviderCallState = "failed"
	// ProviderCallCancelled means the operation was cancelled.
	ProviderCallCancelled ProviderCallState = "cancelled"
)

// Terminal reports whether the state needs no further polling.
func (state ProviderCallState) Terminal() bool {
	return state != ProviderCallRunning
}

// FailureKind is what kind of failure ended a call, in terms a caller can act on.
type FailureKind string

const (
	FailureRefused        FailureKind = "refused"
	FailureIncomplete     FailureKind = "incomplete"
	FailureInvalidRequest FailureKind = "invalid_request"
	FailureFailed         FailureKind = "failed"
)

// Failure explains a call that did not succeed. Message is this service's own wording; the
// provider's raw text never reaches a caller.
type Failure struct {
	Kind    FailureKind
	Message string
}

// ErrProviderRetryable marks a provider interaction that can be retried within its current
// lifecycle step: nothing was accepted, or the operation can be read again.
var ErrProviderRetryable = errors.New("retryable provider failure")

// ErrProviderStartAmbiguous means the provider may have accepted paid work without returning its ID.
var ErrProviderStartAmbiguous = errors.New("provider start outcome is ambiguous")

// ProviderRejectionError is a start the provider refused outright. Nothing ran, so nothing was paid.
type ProviderRejectionError struct {
	Failure Failure
	Err     error
}

func (rejection *ProviderRejectionError) Error() string {
	return "provider rejected the request: " + rejection.Err.Error()
}

func (rejection *ProviderRejectionError) Unwrap() error {
	return rejection.Err
}

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
	// Model and ReasoningEffort are what actually ran, read off the response: a provider may serve a
	// different snapshot or effort than the Tier asked for.
	Model           string
	ReasoningEffort string
	// Output is the JSON document conforming to the requested schema, on success.
	Output json.RawMessage
	// Usage is absent while running, and on a failure that never reached the model.
	Usage *ProviderUsage
	// Failure explains a failed call.
	Failure *Failure
	// Reason is the provider's own account of a failure, for server-side logs only.
	Reason string
	// Retryable permits a fresh attempt after this failure is accounted for.
	Retryable bool
}

// ProviderStartRequest is the input to [Provider.Start].
type ProviderStartRequest struct {
	Tier Tier
	// Instructions are the trusted channel; Input is the untrusted one, any JSON value.
	Instructions string
	Input        json.RawMessage
	// OutputSchema is the JSON Schema the output must conform to.
	OutputSchema json.RawMessage
	// EndUserID identifies the user to the provider's abuse monitoring. The adapter hashes it.
	EndUserID string
	// GenerationID and Attempt are stamped on the provider side so an operation orphaned by a crash
	// stays identifiable.
	GenerationID string
	Attempt      int16
}

// Provider is the narrow surface a check needs. Keeping it to four operations is what leaves room
// for a second provider without rewriting the checks.
type Provider interface {
	// Start begins an operation and returns as soon as the provider accepts it, without waiting for
	// the model. The returned ID must be recorded before anything else happens. An
	// [ErrProviderStartAmbiguous] outcome cannot safely be retried as a new operation.
	Start(ctx context.Context, request *ProviderStartRequest) (*ProviderCall, error)
	// Get reads an operation by id. It is both the poll and the re-attach.
	Get(ctx context.Context, id string) (*ProviderCall, error)
	// Cancel stops an operation. Idempotent — cancelling a terminal operation returns its final
	// state rather than failing.
	Cancel(ctx context.Context, id string) (*ProviderCall, error)
	// Name identifies the provider on the usage record.
	Name() string
}
