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
)

func TestNewGrpcGeneration(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	settledAt := createdAt.Add(time.Minute)
	expiresAt := settledAt.Add(time.Hour)
	message := "the provider failed the call"

	failedResponse := `{"id":"resp_1","status":"failed","error":{"code":"server_error"}}`

	testCases := []struct {
		name string

		generation *core.Generation

		expect *genaiv0.Generation
	}{
		{
			name: "Success/Pending",

			generation: &core.Generation{
				ID: uuid.MustParse(testGenerationID), OwnerID: uuid.MustParse(testOwnerID),
				Purpose: "studio.generation", Provider: "openai",
				Status: core.GenerationStatusPending, CreatedAt: createdAt, UpdatedAt: createdAt,
			},

			expect: &genaiv0.Generation{
				Id: testGenerationID, OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai",
				Status:    genaiv0.GenerationStatus_GENERATION_STATUS_PENDING,
				Attempts:  []*genaiv0.GenerationAttempt{},
				CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
			},
		},
		{
			// A failure still returns every answer the provider gave, and with them what each consumed.
			name: "Success/FailedWithAttempts",

			generation: &core.Generation{
				ID: uuid.MustParse(testGenerationID), OwnerID: uuid.MustParse(testOwnerID),
				Purpose: "studio.generation", Provider: "openai",
				Status: core.GenerationStatusFailed, Error: &message,
				Attempts: []*core.GenerationAttempt{
					{Number: 1, Response: json.RawMessage(failedResponse)},
					{Number: 2, Response: json.RawMessage(failedResponse)},
				},
				CreatedAt: createdAt, UpdatedAt: settledAt, SettledAt: &settledAt, ExpiresAt: &expiresAt,
			},

			expect: &genaiv0.Generation{
				Id: testGenerationID, OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai",
				Status: genaiv0.GenerationStatus_GENERATION_STATUS_FAILED, Error: message,
				Attempts: []*genaiv0.GenerationAttempt{
					{Number: 1, Response: []byte(failedResponse)},
					{Number: 2, Response: []byte(failedResponse)},
				},
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
