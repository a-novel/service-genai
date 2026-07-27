package dao_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationRequeue(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		worker string

		expectErr error
	}{
		{
			name: "Success",

			worker: testWorker,
		},
		{
			name: "Error/NotHeldByThisWorker",

			worker: "someone-else",

			expectErr: dao.ErrGenerationNotHeld,
		},
	}

	daoRequeue := dao.NewGenerationRequeue()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				seedGeneration(ctx, t, 3)
				claimed := claimGenerations(ctx, t)

				_, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
					ID: claimed[0].ID, WorkerID: testWorker, ProviderCallID: "resp_1",
				})
				require.NoError(t, err)

				requeued, err := daoRequeue.Exec(ctx, &dao.GenerationRequeueRequest{
					ID: claimed[0].ID, WorkerID: testCase.worker,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					require.Nil(t, requeued)

					return
				}

				require.Equal(t, dao.GenerationStatusPending, requeued.Status)
				require.Nil(t, requeued.ClaimedBy)
				require.Nil(t, requeued.LeaseExpiresAt)
				// Cleared, unlike a reap: the worker that reported the failure declared its provider
				// operation dead, so the next run starts a fresh one on purpose.
				require.Nil(t, requeued.ProviderCallID)
			})
		})
	}
}
