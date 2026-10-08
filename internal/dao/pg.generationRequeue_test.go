package dao_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationRequeue(t *testing.T) {
	t.Parallel()

	const delay = time.Minute

	testCases := []struct {
		name string

		// state is how far the seeded generation goes before the requeue.
		state   string
		attempt int16
		callID  string

		expectErr error
	}{
		{
			// The failed call is finished, so the next start takes a fresh attempt after the delay.
			name: "Success",

			state:   "running",
			attempt: 1,
			callID:  testCallID,
		},
		{
			name: "Error/OtherAttempt",

			state:   "running",
			attempt: 2,
			callID:  testCallID,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/OtherCall",

			state:   "running",
			attempt: 1,
			callID:  "resp_other",

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/NotRunning",

			state:   "starting",
			attempt: 1,
			callID:  testCallID,

			expectErr: dao.ErrGenerationChanged,
		},
	}

	daoRequeue := dao.NewGenerationRequeue()

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
				}

				requeued, err := daoRequeue.Exec(ctx, &dao.GenerationRequeueRequest{
					ID: generation.ID, Attempt: testCase.attempt, ProviderCallID: testCase.callID, Delay: delay,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, dao.GenerationStatusPending, requeued.Status)
				require.Nil(t, requeued.ProviderCallID)
				require.Nil(t, requeued.StartRequestedAt)
				// The attempt keeps its number: its usage row is keyed by it.
				require.Equal(t, int16(1), requeued.Attempt)
				require.GreaterOrEqual(t, requeued.RunAt.Sub(*generation.StartRequestedAt), delay)
			})
		})
	}
}
