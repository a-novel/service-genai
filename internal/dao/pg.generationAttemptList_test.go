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

func TestPgGenerationAttemptList(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// attempts lists the attempts that recorded an answer, in insertion order.
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

	daoAttemptList := dao.NewPgGenerationAttemptList()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 3)
				other := seedGeneration(ctx, t, 1)

				for _, generationID := range []uuid.UUID{generation.ID, other.ID} {
					for _, attempt := range testCase.attempts {
						_, err := dao.NewPgGenerationAttemptInsert().Exec(ctx, &dao.GenerationAttemptInsertRequest{
							GenerationID: generationID, Attempt: attempt, Response: json.RawMessage(`{}`),
						})
						require.NoError(t, err)
					}
				}

				listed, err := daoAttemptList.Exec(ctx, &dao.GenerationAttemptListRequest{GenerationID: generation.ID})
				require.NoError(t, err)

				attempts := make([]int16, 0, len(listed))
				for _, attempt := range listed {
					require.Equal(t, generation.ID, attempt.GenerationID)

					attempts = append(attempts, attempt.Attempt)
				}

				require.Equal(t, testCase.expectAttempts, attempts)
			})
		})
	}
}
