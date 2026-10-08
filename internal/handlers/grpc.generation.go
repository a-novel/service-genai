package handlers

import (
	"time"

	"github.com/samber/lo"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
	"github.com/a-novel/service-genai/internal/lib"
)

// generationStatuses maps the stored lifecycle onto the wire enum. A status with no mapping reports
// unspecified rather than a plausible neighbour, so a value this service has not published cannot be
// mistaken for one it has.
var generationStatuses = map[core.GenerationStatus]genaiv0.GenerationStatus{
	core.GenerationStatusPending:   genaiv0.GenerationStatus_GENERATION_STATUS_PENDING,
	core.GenerationStatusRunning:   genaiv0.GenerationStatus_GENERATION_STATUS_RUNNING,
	core.GenerationStatusSucceeded: genaiv0.GenerationStatus_GENERATION_STATUS_SUCCEEDED,
	core.GenerationStatusFailed:    genaiv0.GenerationStatus_GENERATION_STATUS_FAILED,
	core.GenerationStatusCancelled: genaiv0.GenerationStatus_GENERATION_STATUS_CANCELLED,
}

// generationFailures maps a failure kind onto the wire enum.
var generationFailures = map[lib.FailureKind]genaiv0.GenerationFailure{
	lib.FailureRefused:        genaiv0.GenerationFailure_GENERATION_FAILURE_REFUSED,
	lib.FailureIncomplete:     genaiv0.GenerationFailure_GENERATION_FAILURE_INCOMPLETE,
	lib.FailureInvalidRequest: genaiv0.GenerationFailure_GENERATION_FAILURE_INVALID_REQUEST,
	lib.FailureFailed:         genaiv0.GenerationFailure_GENERATION_FAILURE_FAILED,
}

// generationTiers maps the wire enum onto a Tier. An unspecified or unknown value has no entry, and
// the core layer refuses the empty Tier it becomes.
var generationTiers = map[genaiv0.Tier]lib.Tier{
	genaiv0.Tier_TIER_FAST:     lib.TierFast,
	genaiv0.Tier_TIER_BALANCED: lib.TierBalanced,
	genaiv0.Tier_TIER_DEEP:     lib.TierDeep,
}

// NewGrpcGeneration converts a stored generation to its wire form.
//
// The request and the provider call identifier are deliberately dropped. The caller already has the
// request it sent, and the identifier is this service's recovery mechanism rather than a caller's
// concern — publishing it would invite a caller to act on it.
func NewGrpcGeneration(generation *core.Generation) *genaiv0.Generation {
	message := &genaiv0.Generation{
		Id:        generation.ID.String(),
		OwnerId:   generation.OwnerID.String(),
		Purpose:   generation.Purpose,
		Status:    generationStatuses[generation.Status],
		Output:    generation.Output,
		Usage:     make([]*genaiv0.GenerationUsage, len(generation.Usage)),
		CreatedAt: generation.CreatedAt.Format(time.RFC3339),
		UpdatedAt: generation.UpdatedAt.Format(time.RFC3339),
	}

	if generation.Failure != nil {
		message.Failure = generationFailures[*generation.Failure]
	}

	if generation.Error != nil {
		message.Error = *generation.Error
	}

	if generation.SettledAt != nil {
		message.SettledAt = generation.SettledAt.Format(time.RFC3339)
	}

	if generation.ExpiresAt != nil {
		message.ExpiresAt = generation.ExpiresAt.Format(time.RFC3339)
	}

	for index, usage := range generation.Usage {
		message.Usage[index] = &genaiv0.GenerationUsage{
			Attempt:           int32(usage.Attempt),
			Provider:          usage.Provider,
			Model:             usage.Model,
			ReasoningEffort:   lo.FromPtr(usage.ReasoningEffort),
			InputTokens:       usage.InputTokens,
			CachedInputTokens: usage.CachedInputTokens,
			CacheWriteTokens:  usage.CacheWriteTokens,
			OutputTokens:      usage.OutputTokens,
			ReasoningTokens:   usage.ReasoningTokens,
		}
	}

	return message
}
