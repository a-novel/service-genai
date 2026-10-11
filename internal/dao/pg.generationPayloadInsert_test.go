package dao_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationPayloadInsert(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// unknownGeneration targets no generation.
		unknownGeneration bool
		payload           json.RawMessage

		expectErr bool
	}{
		{
			// Stored as sent: key order and characters reach the provider unchanged.
			name: "Success",

			payload: json.RawMessage(`{"model": "a-model", "input": "write & read"}`),
		},
		{
			name: "Error/UnknownGeneration",

			unknownGeneration: true,
			payload:           json.RawMessage(`{"model": "a-model"}`),

			expectErr: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generationID := uuid.Must(uuid.NewV7())
				if !testCase.unknownGeneration {
					generation, err := dao.NewPgGenerationSubmit().Exec(ctx, &dao.GenerationSubmitRequest{
						ID: generationID, OwnerID: testOwner, Purpose: "studio.generation", Provider: "openai",
						RequestKey: []byte("the-request-key"), MaxAttempts: 1,
					})
					require.NoError(t, err)
					require.Equal(t, generationID, generation.Generation.ID)
				}

				result, err := dao.NewPgGenerationPayloadInsert().Exec(ctx, &dao.GenerationPayloadInsertRequest{
					GenerationID: generationID, Payload: testCase.payload,
				})

				if testCase.expectErr {
					require.Error(t, err)
					require.Nil(t, result)

					return
				}

				require.NoError(t, err)
				require.Equal(t, generationID, result.GenerationID)
				require.Equal(t, string(testCase.payload), storedPayload(ctx, t, generationID))
			})
		})
	}
}
