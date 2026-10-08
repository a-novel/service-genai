package core

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
)

// Ceilings the configuration and requests are held to. They live here because validation is this
// layer's responsibility; the data access below takes what it is given.
const (
	// SweepIntervalCeiling bounds how long an unpolled generation goes unchecked. A finished
	// background response stays retrievable for only about ten minutes when it is not stored, so the
	// sweep must reach it well inside that window.
	SweepIntervalCeiling = 5 * time.Minute
	// RequestSizeCeiling bounds a request's instructions, input and output schema together. It
	// estimates OpenAI's 922,000-token maximum input using the rough English heuristic of four
	// characters per token and one byte per ASCII character. Tokenization and UTF-8 width vary, so this
	// is a transport and storage bound, not a promise that every request below it fits every model.
	//
	// The ceiling also remains below gRPC's default 4 MiB receive limit, leaving room for the
	// protobuf envelope so an oversized request reaches application validation.
	RequestSizeCeiling = 3_688_000
)

// GenerationStatus identifies where a generation sits in its lifecycle.
type GenerationStatus string

const (
	// GenerationStatusPending means no provider call has been accepted for the current attempt yet.
	GenerationStatusPending GenerationStatus = "pending"
	// GenerationStatusRunning means the provider accepted the call and it has not settled yet.
	GenerationStatusRunning GenerationStatus = "running"
	// GenerationStatusSucceeded means the provider returned a usable output.
	GenerationStatusSucceeded GenerationStatus = "succeeded"
	// GenerationStatusFailed means the generation exhausted its attempts without a usable output.
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
	// Purpose groups the generation under the caller's workflow vocabulary.
	Purpose string
	// Output is the document conforming to the output schema, when the generation succeeds.
	Output json.RawMessage
	// Failure is the kind of failure that ended a failed generation.
	Failure *lib.FailureKind
	// Error names the cause of a failure or cancellation in this service's own words.
	Error *string
	// Status identifies the generation's current lifecycle state.
	Status GenerationStatus
	// Usage is what each provider call consumed, in attempt order.
	Usage []*GenerationUsage
	// CreatedAt is when the generation was submitted.
	CreatedAt time.Time
	// UpdatedAt is when the generation last changed.
	UpdatedAt time.Time
	// SettledAt is when the generation reached a terminal state.
	SettledAt *time.Time
	// ExpiresAt is when the generation content becomes eligible for removal.
	ExpiresAt *time.Time
}

// GenerationUsage is what one provider call consumed, with the model and effort that actually ran.
type GenerationUsage struct {
	Attempt           int16
	Provider          string
	Model             string
	ReasoningEffort   *string
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ReasoningTokens   int64
}

// generationRequest is the provider-neutral request a generation stores and its checks send.
type generationRequest struct {
	Tier         lib.Tier        `json:"tier"`
	Instructions string          `json:"instructions"`
	Input        json.RawMessage `json:"input"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

func newGeneration(generation *dao.Generation, usage []*dao.GenerationUsage) *Generation {
	result := &Generation{
		ID:        generation.ID,
		OwnerID:   generation.OwnerID,
		Purpose:   generation.Purpose,
		Output:    generation.Output,
		Error:     generation.Error,
		Status:    GenerationStatus(generation.Status),
		Usage:     make([]*GenerationUsage, len(usage)),
		CreatedAt: generation.CreatedAt,
		UpdatedAt: generation.UpdatedAt,
		SettledAt: generation.SettledAt,
		ExpiresAt: generation.ExpiresAt,
	}

	if generation.Failure != nil {
		failure := lib.FailureKind(*generation.Failure)
		result.Failure = &failure
	}

	for index, attempt := range usage {
		result.Usage[index] = &GenerationUsage{
			Attempt:           attempt.Attempt,
			Provider:          attempt.Provider,
			Model:             attempt.Model,
			ReasoningEffort:   attempt.ReasoningEffort,
			InputTokens:       attempt.InputTokens,
			CachedInputTokens: attempt.CachedInputTokens,
			OutputTokens:      attempt.OutputTokens,
			ReasoningTokens:   attempt.ReasoningTokens,
		}
	}

	return result
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
