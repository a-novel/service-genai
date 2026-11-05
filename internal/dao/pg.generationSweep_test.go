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

func TestPgGenerationSweep(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// after names the generation the page follows; empty starts the first page.
		after string
		limit int

		// expect lists, by name, the generations read, in id order.
		expect []string
	}{
		{
			// Every unsettled generation, whatever its state; a settled one needs no check.
			name: "Success",

			limit:  10,
			expect: []string{"starting", "running", "unstarted"},
		},
		{
			name: "Success/Limit",

			limit:  2,
			expect: []string{"starting", "running"},
		},
		{
			// The next page follows the last generation of the one before.
			name: "Success/After",

			after:  "running",
			limit:  2,
			expect: []string{"unstarted"},
		},
	}

	daoSweep := dao.NewPgGenerationSweep()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				// Seeded one after the other, so their uuidv7 ids follow the same order.
				seeded := map[string]*dao.Generation{"starting": seedGeneration(ctx, t)}
				seeded["settled"] = settleGeneration(ctx, t, runGeneration(ctx, t, seedGeneration(ctx, t).ID))
				seeded["running"] = runGeneration(ctx, t, seedGeneration(ctx, t).ID)
				seeded["unstarted"] = seedUnstarted(ctx, t)

				names := make(map[uuid.UUID]string, len(seeded))
				for name, generation := range seeded {
					names[generation.ID] = name
				}

				var after uuid.UUID
				if testCase.after != "" {
					after = seeded[testCase.after].ID
				}

				swept, err := daoSweep.Exec(ctx, &dao.GenerationSweepRequest{After: after, Limit: testCase.limit})
				require.NoError(t, err)

				read := make([]string, 0, len(swept))

				for _, generation := range swept {
					read = append(read, names[generation.ID])

					// Each row carries the database clock, which its check measures ages from.
					require.False(t, generation.ReadAt.Before(generation.UpdatedAt))

					// The read writes nothing: the row stands as it was seeded.
					stored, err := dao.NewPgGenerationGet().Exec(ctx, &dao.GenerationGetRequest{
						ID: generation.ID, OwnerID: generation.OwnerID,
					})
					require.NoError(t, err)
					require.Equal(t, generation.UpdatedAt, stored.UpdatedAt)
					require.Equal(t, generation.CheckedAt, stored.CheckedAt)
				}

				require.Equal(t, testCase.expect, read)
			})
		})
	}
}
