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

func TestGenerationRestart(t *testing.T) {
	t.Parallel()

	const epoch = testEpoch + 1

	testCases := []struct {
		name string

		// state is how far the seeded generation goes before the restart.
		state   string
		attempt int16
		callID  *string
		epoch   int32

		expectErr error
	}{
		{
			// The older call is dropped; the next start takes a fresh attempt on the caller's epoch.
			name: "Success/Running",

			state:   "running",
			attempt: 1,
			callID:  new(testCallID),
			epoch:   epoch,
		},
		{
			// A start whose id was never recorded is discarded the same way.
			name: "Success/Starting",

			state:   "starting",
			attempt: 1,
			epoch:   epoch,
		},
		{
			name: "Error/SameEpoch",

			state:   "running",
			attempt: 1,
			callID:  new(testCallID),
			epoch:   testEpoch,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/OtherAttempt",

			state:   "running",
			attempt: 2,
			callID:  new(testCallID),
			epoch:   epoch,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/OtherCall",

			state:   "running",
			attempt: 1,
			callID:  new("resp_other"),
			epoch:   epoch,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// Nothing started, so there is nothing to discard.
			name: "Error/NotStarted",

			epoch: epoch,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/Settled",

			state:   "settled",
			attempt: 1,
			callID:  new(testCallID),
			epoch:   epoch,

			expectErr: dao.ErrGenerationChanged,
		},
	}

	daoRestart := dao.NewGenerationRestart()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 2)

				switch testCase.state {
				case "starting":
					generation = startGeneration(ctx, t, generation.ID)
				case "running":
					generation = runGeneration(ctx, t, generation.ID)
				case "settled":
					generation = settleGeneration(ctx, t, runGeneration(ctx, t, generation.ID))
				}

				restarted, err := daoRestart.Exec(ctx, &dao.GenerationRestartRequest{
					ID:             generation.ID,
					Attempt:        testCase.attempt,
					ProviderCallID: testCase.callID,
					ProviderEpoch:  testCase.epoch,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, dao.GenerationStatusPending, restarted.Status)
				require.Nil(t, restarted.ProviderCallID)
				require.Nil(t, restarted.StartRequestedAt)
				require.Equal(t, new(epoch), restarted.ProviderEpoch)
				// The attempt keeps its number, and the discarded one is not charged to the budget.
				require.Equal(t, int16(1), restarted.Attempt)
				require.Equal(t, generation.MaxAttempts+1, restarted.MaxAttempts)
			})
		})
	}
}
