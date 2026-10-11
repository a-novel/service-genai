package dao_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationUsageInsert(t *testing.T) {
	t.Parallel()

	model := "a-model-snapshot"
	serviceTier := "flex"

	testCases := []struct {
		name string

		// insertTwice replays the same insert, as a settle landing twice would.
		insertTwice bool

		usage *dao.GenerationUsageInsertRequest

		expectErr error
	}{
		{
			name: "Success",

			usage: &dao.GenerationUsageInsertRequest{
				Model: &model, ServiceTier: &serviceTier,
				InputTokens: 1000, CachedInputTokens: 200, CacheWriteTokens: 300,
				OutputTokens: 500, ReasoningTokens: 100,
			},
		},
		{
			// A provider that names no model or processing class still has its usage counted.
			name: "Success/Unnamed",

			usage: &dao.GenerationUsageInsertRequest{InputTokens: 10, OutputTokens: 5},
		},
		{
			name: "Success/NoTokens",

			usage: &dao.GenerationUsageInsertRequest{Model: &model},
		},
		{
			name: "Error/AlreadyRecordedForThisAttempt",

			insertTwice: true,
			usage:       &dao.GenerationUsageInsertRequest{Model: &model, InputTokens: 10, OutputTokens: 5},

			expectErr: dao.ErrGenerationUsageExists,
		},
	}

	daoUsageInsert := dao.NewGenerationUsageInsert()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := runGeneration(ctx, t, seedGeneration(ctx, t, 1).ID)

				request := *testCase.usage
				request.GenerationID = generation.ID
				request.Attempt = generation.Attempt
				request.OwnerID = generation.OwnerID
				request.Purpose = generation.Purpose
				request.Provider = generation.Provider

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

				require.Equal(t, request.Purpose, usage.Purpose)
				require.Equal(t, request.Model, usage.Model)
				require.Equal(t, request.ServiceTier, usage.ServiceTier)
				require.Equal(t, request.InputTokens, usage.InputTokens)
				require.Equal(t, request.CachedInputTokens, usage.CachedInputTokens)
				require.Equal(t, request.CacheWriteTokens, usage.CacheWriteTokens)
				require.Equal(t, request.ReasoningTokens, usage.ReasoningTokens)

				// The purge deletes the generation; its usage stays, for good.
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
