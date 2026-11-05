package dao_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationRecordProviderCall(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// state is how far the seeded generation goes before the record.
		state string

		expectErr error
	}{
		{
			name: "Success",

			state: "starting",
		},
		{
			name: "Error/NeverStarted",

			state: "pending",

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/AlreadyRecorded",

			state: "running",

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// Another check settled the start as unknown while it was in flight. The refusal is how the
			// caller learns its accepted call is an orphan.
			name: "Error/SettledWhileStarting",

			state: "settled",

			expectErr: dao.ErrGenerationChanged,
		},
	}

	daoRecord := dao.NewPgGenerationRecordProviderCall()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				// A submitted generation is starting: its start intent is recorded.
				seed := seedGeneration
				if testCase.state == "pending" {
					seed = seedUnstarted
				}

				generation := seed(ctx, t)

				switch testCase.state {
				case "running":
					runGeneration(ctx, t, generation.ID)
				case "settled":
					settleGeneration(ctx, t, generation)
				}

				recorded, err := daoRecord.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
					ID: generation.ID, ProviderCallID: "resp_new",
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, dao.GenerationStatusRunning, recorded.Status)
				require.Equal(t, "resp_new", *recorded.ProviderCallID)
				require.Equal(t, generation.StartRequestedAt, recorded.StartRequestedAt)
				// A check that stops the call at once measures its ages from the write.
				require.Equal(t, recorded.UpdatedAt, recorded.ReadAt)
			})
		})
	}
}
