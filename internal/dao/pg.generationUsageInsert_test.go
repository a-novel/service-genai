package dao_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationUsageInsert(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// insertTwice replays the same insert, as a settle landing twice would.
		insertTwice bool
		// purgeParent deletes the generation the way the retention purge does.
		purgeParent bool

		usage *dao.GenerationUsageInsertRequest

		expectErr error
	}{
		{
			name: "Success",

			usage: &dao.GenerationUsageInsertRequest{
				Provider: "openai", Model: "a-model-snapshot",
				InputTokens: 1000, CachedInputTokens: 200,
				OutputTokens: 500, ReasoningTokens: 100,
			},
		},
		{
			name: "Success/NoTokens",

			usage: &dao.GenerationUsageInsertRequest{Provider: "openai", Model: "a-model-snapshot"},
		},
		{
			// The row outlives its generation, which is what lets the consumption record be kept
			// while user content is purged. There is deliberately no foreign key.
			name: "Success/SurvivesThePurgeOfItsGeneration",

			purgeParent: true,
			usage: &dao.GenerationUsageInsertRequest{
				Provider: "openai", Model: "a-model-snapshot", InputTokens: 10, OutputTokens: 5,
			},
		},
		{
			name: "Error/AlreadyRecordedForThisAttempt",

			insertTwice: true,
			usage: &dao.GenerationUsageInsertRequest{
				Provider: "openai", Model: "a-model-snapshot", InputTokens: 10, OutputTokens: 5,
			},

			expectErr: dao.ErrGenerationUsageExists,
		},
	}

	daoUsageInsert := dao.NewGenerationUsageInsert()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 1)
				claimed := claimGenerations(ctx, t)

				request := *testCase.usage
				request.GenerationID = claimed[0].ID
				request.Attempt = claimed[0].Attempt
				request.OwnerID = generation.OwnerID
				request.Purpose = generation.Purpose

				if testCase.insertTwice {
					_, err := daoUsageInsert.Exec(ctx, &request)
					require.NoError(t, err)
				}

				usage, err := daoUsageInsert.Exec(ctx, &request)
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					require.Nil(t, usage)

					return
				}

				require.Equal(t, testCase.usage.Model, usage.Model)
				require.Equal(t, testCase.usage.InputTokens, usage.InputTokens)

				if !testCase.purgeParent {
					return
				}

				db, err := postgres.GetContext(ctx)
				require.NoError(t, err)

				_, err = db.NewRaw("DELETE FROM generations WHERE id = ?0", generation.ID).Exec(ctx)
				require.NoError(t, err)

				var remaining int

				err = db.NewRaw(
					"SELECT count(*) FROM generation_usage WHERE generation_id = ?0", generation.ID,
				).Scan(ctx, &remaining)
				require.NoError(t, err)
				require.Equal(t, 1, remaining)
			})
		})
	}
}
