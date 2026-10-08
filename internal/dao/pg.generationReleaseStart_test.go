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

func TestGenerationReleaseStart(t *testing.T) {
	t.Parallel()

	const delay = time.Minute

	testCases := []struct {
		name string

		// state is how far the seeded generation goes before the release.
		state   string
		attempt int16

		expectErr error
	}{
		{
			// The call was never accepted, so the attempt comes back and the next start waits.
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
			name: "Error/NotStarting",

			state:   "pending",
			attempt: 0,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// The provider accepted this attempt. Giving it back would start a second paid call.
			name: "Error/Running",

			state:   "running",
			attempt: 1,

			expectErr: dao.ErrGenerationChanged,
		},
	}

	daoReleaseStart := dao.NewGenerationReleaseStart()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 1)

				switch testCase.state {
				case "starting":
					generation = startGeneration(ctx, t, generation.ID)
				case "running":
					generation = runGeneration(ctx, t, generation.ID)
				}

				released, err := daoReleaseStart.Exec(ctx, &dao.GenerationReleaseStartRequest{
					ID: generation.ID, Attempt: testCase.attempt, Delay: delay,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, dao.GenerationStatusPending, released.Status)
				require.Equal(t, int16(0), released.Attempt)
				require.Nil(t, released.StartRequestedAt)
				require.GreaterOrEqual(t, released.RunAt.Sub(*generation.StartRequestedAt), delay)
			})
		})
	}
}
