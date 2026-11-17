package core

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"

	"github.com/a-novel/service-genai/internal/dao"
)

// PayloadSizeCeiling bounds a payload at 16 MiB: about 3.5 million tokens of prose, over three times
// the largest context OpenAI accepts. It bounds transport, storage and memory, not what a provider
// accepts.
const PayloadSizeCeiling = 16 << 20

// GenerationStatus identifies where a generation sits in its lifecycle.
type GenerationStatus string

const (
	// GenerationStatusPending means the provider has not accepted the generation's call yet.
	GenerationStatusPending GenerationStatus = "pending"
	// GenerationStatusRunning means the provider accepted the call and it has not settled yet.
	GenerationStatusRunning GenerationStatus = "running"
	// GenerationStatusSucceeded means the provider finished the call. Whether its output is usable is
	// the caller's reading of the response.
	GenerationStatusSucceeded GenerationStatus = "succeeded"
	// GenerationStatusFailed means the generation holds no finished response.
	GenerationStatusFailed GenerationStatus = "failed"
	// GenerationStatusCancelled means the generation settled after its owner requested cancellation.
	GenerationStatusCancelled GenerationStatus = "cancelled"
)

// GenerationFailure classifies why a generation failed, so its caller knows whether a resend may
// succeed. This service never resends a call itself.
type GenerationFailure string

const (
	// GenerationFailureRejected means the provider refused the request: the same request fails again.
	GenerationFailureRejected GenerationFailure = "rejected"
	// GenerationFailureProviderFailed means the provider accepted the call, then failed it, did not
	// finish it in time, or no longer held its answer. A resend may succeed.
	GenerationFailureProviderFailed GenerationFailure = "provider_failed"
	// GenerationFailureNotStarted means the provider never ran the call. A resend may succeed.
	GenerationFailureNotStarted GenerationFailure = "not_started"
	// GenerationFailureOutcomeUnknown means the provider may have accepted the call and may still run
	// it. The generation keeps counting toward its purpose's concurrency for a while.
	GenerationFailureOutcomeUnknown GenerationFailure = "outcome_unknown"
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
	// Failure classifies a failed generation's cause. Nil for any other status, and for a generation
	// that failed before failures were classified.
	Failure *GenerationFailure
	// Status identifies the generation's current lifecycle state.
	Status GenerationStatus
	// Response is the provider's answer to the generation's call, as it sent it. Nil until the
	// generation settles, and when the call produced no answer.
	Response json.RawMessage
	// CreatedAt is when the generation was submitted.
	CreatedAt time.Time
	// UpdatedAt is when the generation last changed.
	UpdatedAt time.Time
	// SettledAt is when the generation reached a terminal state.
	SettledAt *time.Time
	// ExpiresAt is when the generation content becomes eligible for removal.
	ExpiresAt *time.Time
}

// newGeneration converts a stored generation and its answer, if any, to the core form.
func newGeneration(generation *dao.Generation, answers []*dao.GenerationAttempt) *Generation {
	return &Generation{
		ID:        generation.ID,
		OwnerID:   generation.OwnerID,
		Purpose:   generation.Purpose,
		Provider:  generation.Provider,
		Error:     generation.Error,
		Failure:   (*GenerationFailure)(generation.Failure),
		Status:    GenerationStatus(generation.Status),
		Response:  lo.LastOr(answers, &dao.GenerationAttempt{}).Response,
		CreatedAt: generation.CreatedAt,
		UpdatedAt: generation.UpdatedAt,
		SettledAt: generation.SettledAt,
		ExpiresAt: generation.ExpiresAt,
	}
}
