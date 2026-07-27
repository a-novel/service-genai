package dao_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationSettle(t *testing.T) {
	t.Parallel()

	failure := "provider refused"

	testCases := []struct {
		name string

		// worker settling. One that does not hold the claim must be refused.
		worker string
		status dao.GenerationStatus
		output json.RawMessage
		error  *string

		expectErr error
	}{
		{
			name: "Success/Succeeded",

			worker: testWorker,
			status: dao.GenerationStatusSucceeded,
			output: json.RawMessage(`{"text": "done"}`),
		},
		{
			// A refusal consumed tokens, so it is a terminal outcome and not an absence of one.
			name: "Success/Failed",

			worker: testWorker,
			status: dao.GenerationStatusFailed,
			error:  &failure,
		},
		{
			name: "Success/Cancelled",

			worker: testWorker,
			status: dao.GenerationStatusCancelled,
		},
		{
			name: "Error/NotHeldByThisWorker",

			worker: "someone-else",
			status: dao.GenerationStatusSucceeded,

			expectErr: dao.ErrGenerationNotHeld,
		},
	}

	daoSettle := dao.NewGenerationSettle()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				seedGeneration(ctx, t, 1)
				claimed := claimGenerations(ctx, t)

				settled, err := daoSettle.Exec(ctx, &dao.GenerationSettleRequest{
					ID:        claimed[0].ID,
					WorkerID:  testCase.worker,
					Status:    testCase.status,
					Output:    testCase.output,
					Error:     testCase.error,
					Retention: testRetention,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					require.Nil(t, settled)

					return
				}

				require.Equal(t, testCase.status, settled.Status)
				require.Nil(t, settled.LeaseExpiresAt)
				// The retention purge reads expires_at, and the terminal-fields constraint refuses a
				// settle that omits either stamp.
				require.NotNil(t, settled.SettledAt)
				require.NotNil(t, settled.ExpiresAt)
			})
		})
	}
}
