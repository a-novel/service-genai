package dao_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationUsageInsert(t *testing.T) {
	t.Parallel()

	tier := "flex"
	startedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(90 * time.Second)

	testCases := []struct {
		name string

		// insertTwice records the attempt's usage first, as the stop of an orphaned call does outside
		// the settle.
		insertTwice bool

		usage *dao.GenerationUsageInsertRequest

		expectErr error
	}{
		{
			name: "Success",

			usage: &dao.GenerationUsageInsertRequest{
				Model: "a-model-snapshot", Class: "high", Tier: &tier,
				InputTokens: 1000, CachedInputTokens: 200, CacheWriteTokens: 300,
				OutputTokens: 500, ReasoningTokens: 100,
				StartedAt: startedAt, CompletedAt: &completedAt,
			},
		},
		{
			// A provider that reports no tier and no completion still has its usage counted.
			name: "Success/Unreported",

			usage: &dao.GenerationUsageInsertRequest{
				Model: "a-model-snapshot", Class: "none", InputTokens: 10, OutputTokens: 5, StartedAt: startedAt,
			},
		},
		{
			name: "Success/NoTokens",

			usage: &dao.GenerationUsageInsertRequest{Model: "a-model-snapshot", Class: "low", StartedAt: startedAt},
		},
		{
			name: "Error/AlreadyRecordedForThisAttempt",

			insertTwice: true,
			usage: &dao.GenerationUsageInsertRequest{
				Model: "a-model-snapshot", Class: "low", InputTokens: 10, OutputTokens: 5, StartedAt: startedAt,
			},

			expectErr: dao.ErrGenerationUsageExists,
		},
		{
			// Cached input is part of the input: a provider count mapped wrong breaks a constraint, which no
			// retry changes.
			name: "Error/Refused",

			usage: &dao.GenerationUsageInsertRequest{
				Model: "a-model-snapshot", Class: "low", InputTokens: 10, CachedInputTokens: 20, StartedAt: startedAt,
			},

			expectErr: dao.ErrRefused,
		},
	}

	daoUsageInsert := dao.NewPgGenerationUsageInsert()

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
				require.Equal(t, request.Class, usage.Class)
				require.Equal(t, request.Tier, usage.Tier)
				require.True(t, request.StartedAt.Equal(usage.StartedAt))
				require.Equal(t, request.CompletedAt == nil, usage.CompletedAt == nil)
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
