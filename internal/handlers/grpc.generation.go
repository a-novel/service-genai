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

// generationFailures maps the failure classes onto the wire enum. A generation without one, or with a
// class this service has not published, reports unspecified.
var generationFailures = map[core.GenerationFailure]genaiv0.GenerationFailure{
	core.GenerationFailureRejected:       genaiv0.GenerationFailure_GENERATION_FAILURE_REJECTED,
	core.GenerationFailureProviderFailed: genaiv0.GenerationFailure_GENERATION_FAILURE_PROVIDER_FAILED,
	core.GenerationFailureNotStarted:     genaiv0.GenerationFailure_GENERATION_FAILURE_NOT_STARTED,
	core.GenerationFailureOutcomeUnknown: genaiv0.GenerationFailure_GENERATION_FAILURE_OUTCOME_UNKNOWN,
}

// NewGrpcGeneration converts a stored generation to its wire form.
func NewGrpcGeneration(generation *core.Generation) *genaiv0.Generation {
	message := &genaiv0.Generation{
		Id:           generation.ID.String(),
		OwnerId:      generation.OwnerID.String(),
		Purpose:      generation.Purpose,
		Provider:     generation.Provider,
		Error:        lo.FromPtr(generation.Error),
		FailureClass: generationFailures[lo.FromPtr(generation.Failure)],
		Status:       generationStatuses[generation.Status],
		Response:     generation.Response,
		Attempts:     []*genaiv0.GenerationAttempt{},
		CreatedAt:    timestamppb.New(generation.CreatedAt),
		UpdatedAt:    timestamppb.New(generation.UpdatedAt),
	}

	if generation.SettledAt != nil {
		message.SettledAt = timestamppb.New(*generation.SettledAt)
	}

	if generation.ExpiresAt != nil {
		message.ExpiresAt = timestamppb.New(*generation.ExpiresAt)
	}

	// Attempts repeats the response for the callers that still read it there.
	if generation.Response != nil {
		message.Attempts = []*genaiv0.GenerationAttempt{{Number: 1, Response: generation.Response}}
	}

	return message
}
