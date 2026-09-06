package handlers_test

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel/service-genai/internal/core"
)

var errFoo = errors.New("foo")

const (
	testGenerationID = "01999999-0000-7000-8000-000000000001"
	testOwnerID      = "00000000-0000-0000-0000-000000000001"
)

// testGeneration is a stored generation in flight, which is the state most handlers see.
func testGeneration() *core.Generation {
	return &core.Generation{
		ID:          uuid.MustParse(testGenerationID),
		OwnerID:     uuid.MustParse(testOwnerID),
		Purpose:     "studio.generation",
		Status:      core.GenerationStatusPending,
		Attempt:     0,
		MaxAttempts: 1,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}
