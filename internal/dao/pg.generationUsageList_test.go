package dao_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationUsageList(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// attempts lists the attempts that recorded usage, in insertion order.
		attempts []int16

		expectAttempts []int16
	}{
		{
			name: "Success",

			attempts:       []int16{2, 1},
			expectAttempts: []int16{1, 2},
		},
		{
			name: "Success/Empty",

			expectAttempts: []int16{},
		},
	}

	daoUsageList := dao.NewGenerationUsageList()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 3)
				other := seedGeneration(ctx, t, 1)

				for _, generationID := range []uuid.UUID{generation.ID, other.ID} {
					for _, attempt := range testCase.attempts {
						_, err := dao.NewGenerationUsageInsert().Exec(ctx, &dao.GenerationUsageInsertRequest{
							GenerationID: generationID, Attempt: attempt, OwnerID: testOwner,
							Purpose: "studio.generation", Provider: "openai", Model: "a-model-snapshot",
						})
						require.NoError(t, err)
					}
				}

				usage, err := daoUsageList.Exec(ctx, &dao.GenerationUsageListRequest{GenerationID: generation.ID})
				require.NoError(t, err)

				attempts := make([]int16, 0, len(usage))
				for _, attempt := range usage {
					require.Equal(t, generation.ID, attempt.GenerationID)

					attempts = append(attempts, attempt.Attempt)
				}

				require.Equal(t, testCase.expectAttempts, attempts)
			})
		})
	}
}
