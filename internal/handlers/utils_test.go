package handlers_test

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

var errFoo = errors.New("foo")

const (
	testGenerationID = "01999999-0000-7000-8000-000000000001"
	testOwnerID      = "00000000-0000-0000-0000-000000000001"
)

// testGeneration is a stored generation in flight, which is the state most handlers see.
func testGeneration() *core.Generation {
	return &core.Generation{
		ID:        uuid.MustParse(testGenerationID),
		OwnerID:   uuid.MustParse(testOwnerID),
		Purpose:   "studio.generation",
		Provider:  "openai",
		Status:    core.GenerationStatusPending,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// testGrpcGeneration is testGeneration as a caller reads it.
func testGrpcGeneration() *genaiv0.Generation {
	return &genaiv0.Generation{
		Id:        testGenerationID,
		OwnerId:   testOwnerID,
		Purpose:   "studio.generation",
		Provider:  "openai",
		Status:    genaiv0.GenerationStatus_GENERATION_STATUS_PENDING,
		Attempts:  []*genaiv0.GenerationAttempt{},
		CreatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		UpdatedAt: timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}
}
