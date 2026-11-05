package dao

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// GenerationStatus is where a generation sits in its lifecycle. It mirrors the generation_status
// database enum, and the two must be migrated together.
type GenerationStatus string

const (
	// GenerationStatusPending means no provider call has been accepted for the current attempt yet.
	GenerationStatusPending GenerationStatus = "pending"
	// GenerationStatusRunning means the provider accepted the call and it has not settled yet.
	GenerationStatusRunning GenerationStatus = "running"
	// GenerationStatusSucceeded means the provider finished the call.
	GenerationStatusSucceeded GenerationStatus = "succeeded"
	// GenerationStatusFailed means the generation holds no finished response.
	GenerationStatusFailed GenerationStatus = "failed"
	// GenerationStatusCancelled means the generation settled after its owner requested cancellation.
	GenerationStatusCancelled GenerationStatus = "cancelled"
)

// GenerationFailure classifies why a generation failed, so its caller knows whether a resend may
// succeed. It mirrors the generation_failure database enum, and the two must be migrated together.
type GenerationFailure string

const (
	// GenerationFailureRejected means the provider refused the request.
	GenerationFailureRejected GenerationFailure = "rejected"
	// GenerationFailureProviderFailed means the provider accepted the call, then failed it, did not
	// finish it in time, or no longer held its answer.
	GenerationFailureProviderFailed GenerationFailure = "provider_failed"
	// GenerationFailureNotStarted means the provider never ran the call.
	GenerationFailureNotStarted GenerationFailure = "not_started"
	// GenerationFailureOutcomeUnknown means the provider may have accepted the call and may still run
	// it.
	GenerationFailureOutcomeUnknown GenerationFailure = "outcome_unknown"
)

// Generation is the durable record of one provider call.
//
// Its attempts hold user content, so the row is purged on a retention schedule and they go with it.
type Generation struct {
	bun.BaseModel `bun:"table:generations,alias:generations"`

	ID      uuid.UUID `bun:"id,pk,type:uuid"`
	OwnerID uuid.UUID `bun:"owner_id,type:uuid"`
	// Purpose is why the caller makes the call, in its own vocabulary.
	Purpose string `bun:"purpose"`
	// Provider names the service the payload is built for.
	Provider string `bun:"provider"`
	// RequestKey identifies the request among live and succeeded generations.
	RequestKey []byte `bun:"request_key"`
	// Error names the cause of a failure or cancellation in this service's own words.
	Error *string `bun:"error,nullzero"`
	// Failure classifies a failed generation's cause. Nil for any other status, and for a generation
	// that failed before failures were classified.
	Failure *GenerationFailure `bun:"failure,nullzero"`

	Status      GenerationStatus `bun:"status"`
	Attempt     int16            `bun:"attempt"`
	MaxAttempts int16            `bun:"max_attempts"`
	// RunAt is the earliest time the next start may be sent.
	RunAt time.Time `bun:"run_at"`
	// StartRequestedAt records that paid work may exist even before its provider ID is durable.
	StartRequestedAt *time.Time `bun:"start_requested_at,nullzero"`
	// FirstStartRequestedAt is when the attempt's first start was requested: the submit records it with
	// the start intent.
	FirstStartRequestedAt *time.Time `bun:"first_start_requested_at,nullzero"`
	ProviderCallID        *string    `bun:"provider_call_id,nullzero"`
	CancelRequestedAt     *time.Time `bun:"cancel_requested_at,nullzero"`
	// CheckedAt is when a check last looked at the generation, by the database clock.
	CheckedAt time.Time `bun:"checked_at"`
	// ReadAt is the database clock when the statement read the row, so a check measures every age on
	// one clock. Only the reads that feed a check fill it.
	ReadAt time.Time `bun:"read_at,scanonly"`
	// Traceparent is the W3C trace context of the request that submitted the generation.
	Traceparent *string `bun:"traceparent,nullzero"`
	// OutcomeUnknownAt is when the generation failed with its start's outcome unknown: the provider
	// may still run a call nothing tracks.
	OutcomeUnknownAt *time.Time `bun:"outcome_unknown_at,nullzero"`
	// Streamed is whether the caller submitted the generation for streaming.
	Streamed bool `bun:"streamed"`

	CreatedAt time.Time  `bun:"created_at"`
	UpdatedAt time.Time  `bun:"updated_at"`
	SettledAt *time.Time `bun:"settled_at,nullzero"`
	ExpiresAt *time.Time `bun:"expires_at,nullzero"`
}
