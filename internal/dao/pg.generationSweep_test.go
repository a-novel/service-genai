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

func TestGenerationSweep(t *testing.T) {
	t.Parallel()

	const interval = time.Minute

	testCases := []struct {
		name string

		limit int

		// expect lists, by name, the generations swept, stalest first.
		expect []string
	}{
		{
			// A fresh generation is someone's poll away from a check, and a settled one needs none.
			name: "Success",

			limit:  10,
			expect: []string{"stalest", "stale"},
		},
		{
			name: "Success/Limit",

			limit:  1,
			expect: []string{"stalest"},
		},
	}

	daoSweep := dao.NewGenerationSweep()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				ids := map[uuid.UUID]string{}

				stalest := runGeneration(ctx, t, seedGeneration(ctx, t, 1).ID)
				setCheckedAt(ctx, t, stalest.ID, -time.Hour)
				ids[stalest.ID] = "stalest"

				stale := seedGeneration(ctx, t, 1)
				setCheckedAt(ctx, t, stale.ID, -2*interval)
				ids[stale.ID] = "stale"

				fresh := seedGeneration(ctx, t, 1)
				ids[fresh.ID] = "fresh"

				settled := settleGeneration(ctx, t, runGeneration(ctx, t, seedGeneration(ctx, t, 1).ID))
				setCheckedAt(ctx, t, settled.ID, -time.Hour)
				ids[settled.ID] = "settled"

				swept, err := daoSweep.Exec(ctx, &dao.GenerationSweepRequest{Interval: interval, Limit: testCase.limit})
				require.NoError(t, err)

				names := make([]string, 0, len(swept))
				for _, generation := range swept {
					names = append(names, ids[generation.ID])
				}

				require.ElementsMatch(t, testCase.expect, names)

				// Swept generations are marked checked, so the next sweep does not take them again.
				again, err := daoSweep.Exec(ctx, &dao.GenerationSweepRequest{Interval: interval, Limit: testCase.limit})
				require.NoError(t, err)

				for _, generation := range again {
					require.NotContains(t, testCase.expect, ids[generation.ID])
				}
			})
		})
	}
}
