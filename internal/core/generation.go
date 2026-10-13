package core

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel/service-genai/internal/dao"
)

// PayloadSizeCeiling bounds a payload at 16 MiB: about 3.5 million tokens of prose, over three times
// the largest context OpenAI accepts. It bounds transport, storage and memory, not what a provider
// accepts.
const PayloadSizeCeiling = 16 << 20

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
	// Purpose is why the caller made the call.
	Purpose string
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
		Purpose:   generation.Purpose,
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

// mayHaveAttempts reports whether a generation can have an attempt answered, so a read with none to
// find is skipped. An answer is recorded when its attempt settles or is requeued: an unsettled
// generation's attempt in flight has none yet.
func mayHaveAttempts(generation *dao.Generation) bool {
	answered := generation.Attempt
	if generation.SettledAt == nil && generation.StartRequestedAt != nil {
		answered--
	}

	return answered > 0
}
