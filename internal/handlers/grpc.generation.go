package handlers

import (
	"github.com/samber/lo"
	"google.golang.org/protobuf/types/known/timestamppb"

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
		CreatedAt: timestamppb.New(generation.CreatedAt),
		UpdatedAt: timestamppb.New(generation.UpdatedAt),
	}

	if generation.SettledAt != nil {
		message.SettledAt = timestamppb.New(*generation.SettledAt)
	}

	if generation.ExpiresAt != nil {
		message.ExpiresAt = timestamppb.New(*generation.ExpiresAt)
	}

	for index, attempt := range generation.Attempts {
		message.Attempts[index] = &genaiv0.GenerationAttempt{
			Number:   int32(attempt.Number),
			Response: attempt.Response,
		}
	}

	return message
}
