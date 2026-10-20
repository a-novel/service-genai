package handlers

import (
	"time"

	"github.com/samber/lo"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// generationStatuses maps the stored lifecycle onto the wire enum. A status with no mapping reports
// unspecified, so a value this service has not published cannot pass for one it has.
var generationStatuses = map[core.GenerationStatus]genaiv0.GenerationStatus{
	core.GenerationStatusPending:   genaiv0.GenerationStatus_GENERATION_STATUS_PENDING,
	core.GenerationStatusRunning:   genaiv0.GenerationStatus_GENERATION_STATUS_RUNNING,
	core.GenerationStatusSucceeded: genaiv0.GenerationStatus_GENERATION_STATUS_SUCCEEDED,
	core.GenerationStatusFailed:    genaiv0.GenerationStatus_GENERATION_STATUS_FAILED,
	core.GenerationStatusCancelled: genaiv0.GenerationStatus_GENERATION_STATUS_CANCELLED,
}

// NewGrpcGeneration converts a stored generation to its wire form.
func NewGrpcGeneration(generation *core.Generation) *genaiv0.Generation {
	message := &genaiv0.Generation{
		Id:        generation.ID.String(),
		OwnerId:   generation.OwnerID.String(),
		Purpose:   generation.Purpose,
		Provider:  generation.Provider,
		Error:     lo.FromPtr(generation.Error),
		Status:    generationStatuses[generation.Status],
		Attempts:  make([]*genaiv0.GenerationAttempt, len(generation.Attempts)),
		CreatedAt: generation.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt: generation.UpdatedAt.Format(time.RFC3339Nano),
	}

	if generation.SettledAt != nil {
		message.SettledAt = generation.SettledAt.Format(time.RFC3339Nano)
	}

	if generation.ExpiresAt != nil {
		message.ExpiresAt = generation.ExpiresAt.Format(time.RFC3339Nano)
	}

	for index, attempt := range generation.Attempts {
		message.Attempts[index] = &genaiv0.GenerationAttempt{
			Number:   int32(attempt.Number),
			Response: attempt.Response,
		}
	}

	return message
}
