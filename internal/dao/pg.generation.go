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
	// GenerationStatusPending means the generation is waiting to be picked up.
	GenerationStatusPending GenerationStatus = "pending"
	// GenerationStatusRunning means a worker holds a lease on the generation and is executing it.
	GenerationStatusRunning GenerationStatus = "running"
	// GenerationStatusSucceeded means the provider returned a usable output.
	GenerationStatusSucceeded GenerationStatus = "succeeded"
	// GenerationStatusFailed means the generation failed with no attempt left to retry.
	GenerationStatusFailed GenerationStatus = "failed"
	// GenerationStatusAbandoned means the lease expired with no attempt remaining — the worker
	// executing it died mid-run.
	GenerationStatusAbandoned GenerationStatus = "abandoned"
	// GenerationStatusCancelled means the generation was settled on the owner's request rather than
	// by running to completion.
	GenerationStatusCancelled GenerationStatus = "cancelled"
)

// Generation is the durable record of one AI generative call, from submission to terminal outcome.
//
// Request and Output hold user content, which is why this row is purged on a retention schedule
// while the cost rows describing it are kept indefinitely.
//
// The generation_ledger table ships in the same migration but has no model here yet: it gains one
// with the settle operation that writes it, which is also where the exact-decimal representation
// its money columns need gets chosen.
type Generation struct {
	bun.BaseModel `bun:"table:generations,alias:generations"`

	ID      uuid.UUID `bun:"id,pk,type:uuid"`
	OwnerID uuid.UUID `bun:"owner_id,type:uuid"`
	Purpose string    `bun:"purpose"`
	Profile string    `bun:"profile"`

	IdempotencyKey     string          `bun:"idempotency_key"`
	RequestFingerprint []byte          `bun:"request_fingerprint"`
	Request            json.RawMessage `bun:"request,type:jsonb"`
	Output             json.RawMessage `bun:"output,type:jsonb,nullzero"`
	// Error is the serialised failure. Opaque here: nothing queries inside it.
	Error *string `bun:"error,nullzero"`

	Status      GenerationStatus `bun:"status"`
	Attempt     int16            `bun:"attempt"`
	MaxAttempts int16            `bun:"max_attempts"`

	RunAt             time.Time  `bun:"run_at"`
	LeaseExpiresAt    *time.Time `bun:"lease_expires_at,nullzero"`
	ClaimedBy         *string    `bun:"claimed_by,nullzero"`
	CancelRequestedAt *time.Time `bun:"cancel_requested_at,nullzero"`
	ProviderCallID    *string    `bun:"provider_call_id,nullzero"`

	CreatedAt time.Time  `bun:"created_at"`
	UpdatedAt time.Time  `bun:"updated_at"`
	SettledAt *time.Time `bun:"settled_at,nullzero"`
	ExpiresAt *time.Time `bun:"expires_at,nullzero"`
}
