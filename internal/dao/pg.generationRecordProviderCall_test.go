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

func TestGenerationRecordProviderCall(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// state is how far the seeded generation goes before the record.
		state   string
		attempt int16

		expectErr error
	}{
		{
			name: "Success",

			state:   "starting",
			attempt: 1,
		},
		{
			name: "Error/OtherAttempt",

			state:   "starting",
			attempt: 2,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/NeverStarted",

			state:   "pending",
			attempt: 0,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/AlreadyRecorded",

			state:   "running",
			attempt: 1,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// Another check settled the attempt as unknown while this start was in flight. The
			// refusal is how the caller learns its accepted call is an orphan.
			name: "Error/SettledWhileStarting",

			state:   "settled",
			attempt: 1,

			expectErr: dao.ErrGenerationChanged,
		},
	}

	daoRecord := dao.NewGenerationRecordProviderCall()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 1)

				switch testCase.state {
				case "starting":
					startGeneration(ctx, t, generation.ID)
				case "running":
					runGeneration(ctx, t, generation.ID)
				case "settled":
					settleGeneration(ctx, t, startGeneration(ctx, t, generation.ID))
				}

				recorded, err := daoRecord.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
					ID: generation.ID, Attempt: testCase.attempt, ProviderCallID: "resp_new",
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, dao.GenerationStatusRunning, recorded.Status)
				require.Equal(t, "resp_new", *recorded.ProviderCallID)
				require.Equal(t, testCase.attempt, recorded.Attempt)
			})
		})
	}
}
