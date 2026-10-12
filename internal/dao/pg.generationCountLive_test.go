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

func TestPgGenerationCountLive(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		owner   uuid.UUID
		purpose *string
		// unknownSince seeds a generation that failed with its start's outcome unknown, that long ago.
		unknownSince time.Duration
		// noWindow counts with a zero unknown-outcome window, as the global scope does.
		noWindow bool

		expect int64
	}{
		{
			// A pending and a running generation; the settled one no longer runs.
			name: "Success",

			owner: testOwner,

			expect: 2,
		},
		{
			// The running generation was submitted for another purpose.
			name: "Success/OnePurpose",

			owner:   testOwner,
			purpose: new("studio.generation"),

			expect: 1,
		},
		{
			// The provider may still run its call, so it holds a slot within the window.
			name: "Success/UnknownOutcomeHeld",

			owner:        testOwner,
			unknownSince: time.Minute,

			expect: 3,
		},
		{
			name: "Success/UnknownOutcomeWithoutWindow",

			owner:        testOwner,
			unknownSince: time.Minute,
			noWindow:     true,

			expect: 2,
		},
		{
			name: "Success/UnknownOutcomeReleased",

			owner:        testOwner,
			unknownSince: time.Hour,

			expect: 2,
		},
		{
			name: "Success/OtherOwner",

			owner: uuid.MustParse("00000000-0000-0000-0000-000000000002"),

			expect: 0,
		},
	}

	daoCountLive := dao.NewPgGenerationCountLive()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				seedGeneration(ctx, t, 1)
				running := runGeneration(ctx, t, seedGeneration(ctx, t, 1).ID)
				execute(ctx, t, "UPDATE generations SET purpose = 'studio.analysis' WHERE id = ?0", running.ID)
				settleGeneration(ctx, t, runGeneration(ctx, t, seedGeneration(ctx, t, 1).ID))

				if testCase.unknownSince > 0 {
					unknown := settleGeneration(ctx, t, startGeneration(ctx, t, seedGeneration(ctx, t, 1).ID))
					execute(ctx, t,
						"UPDATE generations SET outcome_unknown_at = clock_timestamp() - make_interval(secs => ?1) WHERE id = ?0",
						unknown.ID, testCase.unknownSince.Seconds(),
					)
				}

				window := 15 * time.Minute
				if testCase.noWindow {
					window = 0
				}

				live, err := daoCountLive.Exec(ctx, &dao.GenerationCountLiveRequest{
					OwnerID: testCase.owner, Purpose: testCase.purpose, UnknownOutcomeWindow: window,
				})
				require.NoError(t, err)
				require.Equal(t, testCase.expect, live.Count)
			})
		})
	}
}
