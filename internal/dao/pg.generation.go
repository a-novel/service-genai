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
	// GenerationStatusSucceeded means the provider returned a usable output.
	GenerationStatusSucceeded GenerationStatus = "succeeded"
	// GenerationStatusFailed means the generation failed with no attempt left to retry.
	GenerationStatusFailed GenerationStatus = "failed"
	// GenerationStatusCancelled means the generation was settled on the owner's request rather than
	// by running to completion.
	GenerationStatusCancelled GenerationStatus = "cancelled"
)

// Generation is the durable record of one AI generative call.
//
// Request and Output hold user content, so this row is purged on a retention schedule while the
// usage rows describing it are kept. The generation_usage table gains its model with the settle
// operation that writes it.
type Generation struct {
	bun.BaseModel `bun:"table:generations,alias:generations"`

	ID      uuid.UUID `bun:"id,pk,type:uuid"`
	OwnerID uuid.UUID `bun:"owner_id,type:uuid"`
	Purpose string    `bun:"purpose"`

	// RequestKey identifies the request among live and succeeded generations.
	RequestKey []byte `bun:"request_key"`
	// Request is the caller's provider-neutral request, stored as sent.
	Request json.RawMessage `bun:"request,type:json"`
	Output  json.RawMessage `bun:"output,type:json,nullzero"`
	// Failure is the kind of failure that ended a failed generation.
	Failure *string `bun:"failure,nullzero"`
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

	CreatedAt time.Time  `bun:"created_at"`
	UpdatedAt time.Time  `bun:"updated_at"`
	SettledAt *time.Time `bun:"settled_at,nullzero"`
	ExpiresAt *time.Time `bun:"expires_at,nullzero"`
}
