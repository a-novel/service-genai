package core

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel/service-genai/internal/dao"
)

// Ceilings the configuration and requests are held to. They live here because validation is this
// layer's responsibility; the data access below takes what it is given.
const (
	// SweepIntervalCeiling bounds how long an unpolled generation goes unchecked. A finished
	// background response stays retrievable for only a limited time when it is not stored, so the
	// sweep must reach it well inside that window.
	SweepIntervalCeiling = 5 * time.Minute
	// PayloadSizeCeiling bounds a payload. It stays below gRPC's default 4 MiB receive limit, leaving
	// room for the protobuf envelope, so an oversized payload reaches application validation. It
	// bounds transport and storage, not what a provider accepts.
	PayloadSizeCeiling = 3_688_000
)

// GenerationStatus identifies where a generation sits in its lifecycle.
type GenerationStatus string

const (
	// GenerationStatusPending means no provider call has been accepted for the current attempt yet.
	GenerationStatusPending GenerationStatus = "pending"
	// GenerationStatusRunning means the provider accepted the call and it has not settled yet.
	GenerationStatusRunning GenerationStatus = "running"
	// GenerationStatusSucceeded means the provider finished the call. Whether its output is usable is
	// the caller's reading of the last attempt's response.
	GenerationStatusSucceeded GenerationStatus = "succeeded"
	// GenerationStatusFailed means the generation holds no finished response.
	GenerationStatusFailed GenerationStatus = "failed"
	// GenerationStatusCancelled means the generation settled after its owner requested cancellation.
	GenerationStatusCancelled GenerationStatus = "cancelled"
)

// Generation is the core result exposed to generation transports.
type Generation struct {
	// ID identifies the generation.
	ID uuid.UUID
	// OwnerID identifies the user who owns the generation.
	OwnerID uuid.UUID
	// Provider names the service the generation runs on.
	Provider string
	// Error names the cause of a failure or cancellation in this service's own words.
	Error *string
	// Status identifies the generation's current lifecycle state.
	Status GenerationStatus
	// Attempts are the provider's answers, in attempt order.
	Attempts []*GenerationAttempt
	// CreatedAt is when the generation was submitted.
	CreatedAt time.Time
	// UpdatedAt is when the generation last changed.
	UpdatedAt time.Time
	// SettledAt is when the generation reached a terminal state.
	SettledAt *time.Time
	// ExpiresAt is when the generation content becomes eligible for removal.
	ExpiresAt *time.Time
}

// GenerationAttempt is the provider's answer to one attempt.
type GenerationAttempt struct {
	Number int16
	// Response is the provider's final answer, as it sent it.
	Response json.RawMessage
}

func newGeneration(generation *dao.Generation, attempts []*dao.GenerationAttempt) *Generation {
	result := &Generation{
		ID:        generation.ID,
		OwnerID:   generation.OwnerID,
		Provider:  generation.Provider,
		Error:     generation.Error,
		Status:    GenerationStatus(generation.Status),
		Attempts:  make([]*GenerationAttempt, len(attempts)),
		CreatedAt: generation.CreatedAt,
		UpdatedAt: generation.UpdatedAt,
		SettledAt: generation.SettledAt,
		ExpiresAt: generation.ExpiresAt,
	}

	for index, attempt := range attempts {
		result.Attempts[index] = &GenerationAttempt{Number: attempt.Attempt, Response: attempt.Response}
	}

	return result
}

// Errors a caller can act on. Everything else is a fault.
var (
	// ErrGenerationNotFound is returned when the owner has no such generation. A generation owned by
	// somebody else reports this too, so an identifier cannot be probed for existence.
	ErrGenerationNotFound = errors.New("generation not found")
)
