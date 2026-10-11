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

func TestPgGenerationCountLive(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		owner   uuid.UUID
		purpose *string

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

				live, err := daoCountLive.Exec(ctx, &dao.GenerationCountLiveRequest{
					OwnerID: testCase.owner, Purpose: testCase.purpose,
				})
				require.NoError(t, err)
				require.Equal(t, testCase.expect, live.Count)
			})
		})
	}
}
