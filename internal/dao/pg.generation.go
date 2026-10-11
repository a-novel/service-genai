package dao

import (
	"encoding/json"
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
	// GenerationStatusFailed means the generation failed with no attempt left to retry.
	GenerationStatusFailed GenerationStatus = "failed"
	// GenerationStatusCancelled means the generation was settled on the owner's request rather than
	// by running to completion.
	GenerationStatusCancelled GenerationStatus = "cancelled"
)

// Generation is the durable record of one provider call.
//
// Payload, and the attempts recorded against the row, hold user content, so the row is purged on a
// retention schedule and its attempts with it.
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
	// Payload is the provider's own request, built by the caller and stored as sent.
	Payload json.RawMessage `bun:"payload,type:json"`
	// Error names the cause of a failure or cancellation in this service's own words.
	Error *string `bun:"error,nullzero"`

	Status      GenerationStatus `bun:"status"`
	Attempt     int16            `bun:"attempt"`
	MaxAttempts int16            `bun:"max_attempts"`
	// RunAt is the earliest time the next start may be sent.
	RunAt time.Time `bun:"run_at"`
	// StartRequestedAt records that paid work may exist even before its provider ID is durable.
	StartRequestedAt  *time.Time `bun:"start_requested_at,nullzero"`
	ProviderCallID    *string    `bun:"provider_call_id,nullzero"`
	CancelRequestedAt *time.Time `bun:"cancel_requested_at,nullzero"`
	// CheckedAt is when a check last looked at the generation, by the database clock.
	CheckedAt time.Time `bun:"checked_at"`
	// Traceparent is the W3C trace context of the request that submitted the generation.
	Traceparent *string `bun:"traceparent,nullzero"`

	CreatedAt time.Time  `bun:"created_at"`
	UpdatedAt time.Time  `bun:"updated_at"`
	SettledAt *time.Time `bun:"settled_at,nullzero"`
	ExpiresAt *time.Time `bun:"expires_at,nullzero"`
}
