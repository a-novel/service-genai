package dao_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationUsageSum(t *testing.T) {
	t.Parallel()

	otherOwner := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	generation, analysis := "studio.generation", "studio.analysis"

	testCases := []struct {
		name string

		purpose *string
		window  time.Duration

		expect dao.GenerationUsageTotals
	}{
		{
			// Within the hour: the recent generation and the recent analysis, not the old one.
			name: "Success/AllPurposes",

			window: time.Hour,

			expect: dao.GenerationUsageTotals{InputTokens: 300, OutputTokens: 30},
		},
		{
			name: "Success/OnePurpose",

			purpose: &generation,
			window:  time.Hour,

			expect: dao.GenerationUsageTotals{InputTokens: 100, OutputTokens: 10},
		},
		{
			// The day reaches the old record too.
			name: "Success/LongerWindow",

			purpose: &generation,
			window:  24 * time.Hour,

			expect: dao.GenerationUsageTotals{InputTokens: 1100, OutputTokens: 110},
		},
		{
			// The analysis is twenty minutes old.
			name: "Success/Nothing",

			purpose: &analysis,
			window:  time.Minute,

			expect: dao.GenerationUsageTotals{},
		},
	}

	daoUsageSum := dao.NewPgGenerationUsageSum()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				// The test owner's usage: a generation and an analysis within the hour, a generation
				// two hours old. Another owner's usage is never counted.
				seedUsage(ctx, t, testOwner, generation, 100, 10, -10*time.Minute)
				seedUsage(ctx, t, testOwner, analysis, 200, 20, -20*time.Minute)
				seedUsage(ctx, t, testOwner, generation, 1000, 100, -2*time.Hour)
				seedUsage(ctx, t, otherOwner, generation, 5000, 500, -10*time.Minute)

				totals, err := daoUsageSum.Exec(ctx, &dao.GenerationUsageSumRequest{
					OwnerID: testOwner, Purpose: testCase.purpose, Window: testCase.window,
				})
				require.NoError(t, err)
				require.Equal(t, testCase.expect, *totals)
			})
		})
	}
}
