package core

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel/service-genai/internal/dao"
)

// Ceilings the worker configuration is held to. They live here because validation is this layer's
// responsibility; the data access below takes what it is given.
//
// Request-shape ceilings are expressed as validate tags on the request structs instead, since the
// tag syntax takes literals.
const (
	// ClaimLeaseCeiling bounds a worker's lease. Longer than this outlives any generation we run,
	// so a stranded claim would sit invisible for an hour.
	ClaimLeaseCeiling = time.Hour
	// ClaimLimitCeiling bounds one claim, so a single worker cannot take the whole queue.
	ClaimLimitCeiling = 100
	// RequestSizeCeiling bounds a submitted provider payload. It estimates OpenAI's 922,000-token
	// maximum input using the rough English heuristic of four characters per token and one byte per
	// ASCII character, leaving the model's separate 128,000-token output allowance untouched.
	// Tokenization and UTF-8 width vary, so this is a transport and storage bound, not a promise that
	// every payload below it fits every model.
	//
	// The ceiling also remains below gRPC's default 4 MiB receive limit, leaving room for the
	// protobuf envelope so an oversized request reaches application validation.
	RequestSizeCeiling = 3_688_000
)

// GenerationStatus identifies where a generation sits in its lifecycle.
type GenerationStatus string

const (
	// GenerationStatusPending means the generation is waiting for a worker.
	GenerationStatusPending GenerationStatus = "pending"
	// GenerationStatusRunning means a worker is executing the generation.
	GenerationStatusRunning GenerationStatus = "running"
	// GenerationStatusSucceeded means the provider returned a usable output.
	GenerationStatusSucceeded GenerationStatus = "succeeded"
	// GenerationStatusFailed means the generation exhausted its attempts without a usable output.
	GenerationStatusFailed GenerationStatus = "failed"
	// GenerationStatusAbandoned means the final worker lease expired before settlement.
	GenerationStatusAbandoned GenerationStatus = "abandoned"
	// GenerationStatusCancelled means the generation settled after its owner requested cancellation.
	GenerationStatusCancelled GenerationStatus = "cancelled"
)

// Generation is the core result exposed to generation transports.
type Generation struct {
	// ID identifies the generation.
	ID uuid.UUID
	// OwnerID identifies the user who owns the generation.
	OwnerID uuid.UUID
	// Purpose groups the generation under the caller's workflow vocabulary.
	Purpose string
	// Output is the provider response when the generation succeeds.
	Output json.RawMessage
	// Error is the serialized failure when the generation does not succeed.
	Error *string
	// Status identifies the generation's current lifecycle state.
	Status GenerationStatus
	// Attempt is the zero-based attempt currently running or most recently completed.
	Attempt int16
	// MaxAttempts caps the number of provider attempts.
	MaxAttempts int16
	// CreatedAt is when the generation was submitted.
	CreatedAt time.Time
	// UpdatedAt is when the generation last changed.
	UpdatedAt time.Time
	// SettledAt is when the generation reached a terminal state.
	SettledAt *time.Time
	// ExpiresAt is when the generation content becomes eligible for removal.
	ExpiresAt *time.Time
}

func newGeneration(generation *dao.Generation) *Generation {
	if generation == nil {
		return nil
	}

	return &Generation{
		ID:          generation.ID,
		OwnerID:     generation.OwnerID,
		Purpose:     generation.Purpose,
		Output:      generation.Output,
		Error:       generation.Error,
		Status:      GenerationStatus(generation.Status),
		Attempt:     generation.Attempt,
		MaxAttempts: generation.MaxAttempts,
		CreatedAt:   generation.CreatedAt,
		UpdatedAt:   generation.UpdatedAt,
		SettledAt:   generation.SettledAt,
		ExpiresAt:   generation.ExpiresAt,
	}
}

// Errors a caller can act on. Everything else is a fault.
var (
	// ErrGenerationNotFound is returned when the owner has no such generation. A generation owned by
	// somebody else reports this too, so an identifier cannot be probed for existence.
	ErrGenerationNotFound = errors.New("generation not found")
	// ErrGenerationNotCancellable is returned when a generation cannot be stopped, because it does
	// not exist for this owner or has already settled.
	ErrGenerationNotCancellable = errors.New("generation cannot be cancelled")
	// ErrIdempotencyConflict is returned when an idempotency key is reused with a different request.
	ErrIdempotencyConflict = errors.New("idempotency key already used with a different request")
)
