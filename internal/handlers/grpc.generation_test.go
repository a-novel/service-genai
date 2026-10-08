package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
	"github.com/a-novel/service-genai/internal/lib"
)

func TestNewGrpcGeneration(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	settledAt := createdAt.Add(time.Minute)
	expiresAt := settledAt.Add(time.Hour)
	refused := lib.FailureRefused
	message := "the model refused the request"
	effort := "medium"

	testCases := []struct {
		name string

		generation *core.Generation

		expect *genaiv0.Generation
	}{
		{
			name: "Success/Pending",

			generation: &core.Generation{
				ID: uuid.MustParse(testGenerationID), OwnerID: uuid.MustParse(testOwnerID), Purpose: "studio.generation",
				Status: core.GenerationStatusPending, CreatedAt: createdAt, UpdatedAt: createdAt,
			},

			expect: &genaiv0.Generation{
				Id: testGenerationID, OwnerId: testOwnerID, Purpose: "studio.generation",
				Status: genaiv0.GenerationStatus_GENERATION_STATUS_PENDING, Usage: []*genaiv0.GenerationUsage{},
				CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
			},
		},
		{
			// A failure still reports what it consumed, with the model and effort that actually ran.
			name: "Success/FailedWithUsage",

			generation: &core.Generation{
				ID: uuid.MustParse(testGenerationID), OwnerID: uuid.MustParse(testOwnerID), Purpose: "studio.generation",
				Status: core.GenerationStatusFailed, Failure: &refused, Error: &message,
				Usage: []*core.GenerationUsage{
					{
						Attempt: 1, Provider: "openai", Model: "gpt-5.6-terra-2026-01-01", ReasoningEffort: &effort,
						InputTokens: 1000, CachedInputTokens: 200, OutputTokens: 500, ReasoningTokens: 100,
					},
					{Attempt: 2, Provider: "openai", Model: "gpt-5.6-terra-2026-01-01", InputTokens: 10},
				},
				CreatedAt: createdAt, UpdatedAt: settledAt, SettledAt: &settledAt, ExpiresAt: &expiresAt,
			},

			expect: &genaiv0.Generation{
				Id: testGenerationID, OwnerId: testOwnerID, Purpose: "studio.generation",
				Status:  genaiv0.GenerationStatus_GENERATION_STATUS_FAILED,
				Failure: genaiv0.GenerationFailure_GENERATION_FAILURE_REFUSED, Error: message,
				Usage: []*genaiv0.GenerationUsage{
					{
						Attempt: 1, Provider: "openai", Model: "gpt-5.6-terra-2026-01-01", ReasoningEffort: "medium",
						InputTokens: 1000, CachedInputTokens: 200, OutputTokens: 500, ReasoningTokens: 100,
					},
					{Attempt: 2, Provider: "openai", Model: "gpt-5.6-terra-2026-01-01", InputTokens: 10},
				},
				CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:01:00Z",
				SettledAt: "2026-01-01T00:01:00Z", ExpiresAt: "2026-01-01T01:01:00Z",
			},
		},
		{
			name: "Success/Succeeded",

			generation: &core.Generation{
				ID: uuid.MustParse(testGenerationID), OwnerID: uuid.MustParse(testOwnerID), Purpose: "studio.generation",
				Status: core.GenerationStatusSucceeded, Output: json.RawMessage(`{"text":"done"}`),
				Usage:     []*core.GenerationUsage{},
				CreatedAt: createdAt, UpdatedAt: settledAt, SettledAt: &settledAt, ExpiresAt: &expiresAt,
			},

			expect: &genaiv0.Generation{
				Id: testGenerationID, OwnerId: testOwnerID, Purpose: "studio.generation",
				Status: genaiv0.GenerationStatus_GENERATION_STATUS_SUCCEEDED, Output: []byte(`{"text":"done"}`),
				Usage:     []*genaiv0.GenerationUsage{},
				CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:01:00Z",
				SettledAt: "2026-01-01T00:01:00Z", ExpiresAt: "2026-01-01T01:01:00Z",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expect, handlers.NewGrpcGeneration(testCase.generation))
		})
	}
}
