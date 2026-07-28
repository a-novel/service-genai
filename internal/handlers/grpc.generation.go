package handlers

import (
	"time"

	"github.com/a-novel/service-genai/internal/dao"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// generationStatuses maps the stored lifecycle onto the wire enum. A status with no mapping reports
// unspecified rather than a plausible neighbour, so a value this service has not published cannot be
// mistaken for one it has.
var generationStatuses = map[dao.GenerationStatus]genaiv0.GenerationStatus{
	dao.GenerationStatusPending:   genaiv0.GenerationStatus_GENERATION_STATUS_PENDING,
	dao.GenerationStatusRunning:   genaiv0.GenerationStatus_GENERATION_STATUS_RUNNING,
	dao.GenerationStatusSucceeded: genaiv0.GenerationStatus_GENERATION_STATUS_SUCCEEDED,
	dao.GenerationStatusFailed:    genaiv0.GenerationStatus_GENERATION_STATUS_FAILED,
	dao.GenerationStatusAbandoned: genaiv0.GenerationStatus_GENERATION_STATUS_ABANDONED,
	dao.GenerationStatusCancelled: genaiv0.GenerationStatus_GENERATION_STATUS_CANCELLED,
}

// NewGrpcGeneration converts a stored generation to its wire form.
//
// The request and the provider call identifier are deliberately dropped. The caller already has the
// request it sent, and the identifier is this service's recovery mechanism rather than a caller's
// concern — publishing it would invite a caller to act on it.
func NewGrpcGeneration(generation *dao.Generation) *genaiv0.Generation {
	message := &genaiv0.Generation{
		Id:          generation.ID.String(),
		OwnerId:     generation.OwnerID.String(),
		Purpose:     generation.Purpose,
		Status:      generationStatuses[generation.Status],
		Attempt:     int32(generation.Attempt),
		MaxAttempts: int32(generation.MaxAttempts),
		Output:      generation.Output,
		CreatedAt:   generation.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   generation.UpdatedAt.Format(time.RFC3339),
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

	return message
}
