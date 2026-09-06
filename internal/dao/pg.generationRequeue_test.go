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

func TestGenerationRequeue(t *testing.T) {
	t.Parallel()

	providerCallID := "resp_1"

	testCases := []struct {
		name string

		worker                 string
		recordedProviderCallID *string
		requestProviderCallID  *string

		expectErr error
	}{
		{
			name: "Success",

			worker: testWorker,
		},
		{
			name: "Success/KnownTerminalProviderOperation",

			worker:                 testWorker,
			recordedProviderCallID: &providerCallID,
			requestProviderCallID:  &providerCallID,
		},
		{
			name: "Error/KnownProviderOperationWithoutAuthorization",

			worker:                 testWorker,
			recordedProviderCallID: &providerCallID,

			expectErr: dao.ErrGenerationNotHeld,
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

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				seedGeneration(ctx, t, 3)
				claimed := claimGenerations(ctx, t)

				if testCase.recordedProviderCallID != nil {
					_, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
						ClaimToken: claimed[0].ClaimToken,
						ID:         claimed[0].ID, WorkerID: testWorker, ProviderCallID: *testCase.recordedProviderCallID,
					})
					require.NoError(t, err)
				}

				requeued, err := daoRequeue.Exec(ctx, &dao.GenerationRequeueRequest{
					ClaimToken: claimed[0].ClaimToken,
					ID:         claimed[0].ID, WorkerID: testCase.worker, ProviderCallID: testCase.requestProviderCallID,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					require.Nil(t, requeued)

					return
				}

				require.Equal(t, dao.GenerationStatusPending, requeued.Status)
				require.Nil(t, requeued.ClaimedBy)
				require.Nil(t, requeued.LeaseExpiresAt)
				require.Nil(t, requeued.ProviderCallID)

				reclaimed := claimGenerations(ctx, t)
				require.Len(t, reclaimed, 1)
				require.Equal(t, claimed[0].Attempt+1, reclaimed[0].Attempt)
				require.Nil(t, reclaimed[0].ProviderCallID)
			})
		})
	}
}
