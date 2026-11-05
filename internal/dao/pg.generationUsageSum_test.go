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

	// totals is what a case expects of one window: its sums, and how long ago its oldest record was
	// written, zero when it holds none.
	type totals struct {
		input, output int64
		oldest        time.Duration
	}

	testCases := []struct {
		name string

		purpose *string
		windows []time.Duration

		expect []totals
	}{
		{
			// Within the hour: the recent generation and the recent analysis, not the old one.
			name: "Success/AllPurposes",

			windows: []time.Duration{time.Hour},

			expect: []totals{{input: 300, output: 30, oldest: 20 * time.Minute}},
		},
		{
			name: "Success/OnePurpose",

			purpose: &generation,
			windows: []time.Duration{time.Hour},

			expect: []totals{{input: 100, output: 10, oldest: 10 * time.Minute}},
		},
		{
			// The day reaches the old record too.
			name: "Success/LongerWindow",

			purpose: &generation,
			windows: []time.Duration{24 * time.Hour},

			expect: []totals{{input: 1100, output: 110, oldest: 2 * time.Hour}},
		},
		{
			// The analysis is twenty minutes old.
			name: "Success/Nothing",

			purpose: &analysis,
			windows: []time.Duration{time.Minute},

			expect: []totals{{}},
		},
		{
			// One query sums every window, each in the order given, an empty one included.
			name: "Success/SeveralWindows",

			windows: []time.Duration{24 * time.Hour, time.Minute, time.Hour},

			expect: []totals{
				{input: 1300, output: 130, oldest: 2 * time.Hour},
				{},
				{input: 300, output: 30, oldest: 20 * time.Minute},
			},
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

				summed, err := daoUsageSum.Exec(ctx, &dao.GenerationUsageSumRequest{
					OwnerID: testOwner, Purpose: testCase.purpose, Windows: testCase.windows,
				})
				require.NoError(t, err)
				require.Len(t, summed, len(testCase.expect))

				for index, expected := range testCase.expect {
					require.Equal(t, expected.input, summed[index].InputTokens)
					require.Equal(t, expected.output, summed[index].OutputTokens)

					if expected.oldest == 0 {
						require.Nil(t, summed[index].OldestAt)

						continue
					}

					require.NotNil(t, summed[index].OldestAt)
					require.WithinDuration(t, time.Now().Add(-expected.oldest), *summed[index].OldestAt, 10*time.Second)
				}
			})
		})
	}
}
