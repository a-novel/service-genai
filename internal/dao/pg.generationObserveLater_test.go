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

func TestGenerationObserveLater(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		worker             string
		recordProviderCall bool

		expectErr error
	}{
		{
			name:               "Success",
			worker:             testWorker,
			recordProviderCall: true,
		},
		{
			name: "Error/NoKnownProviderOperation",

			worker: testWorker,

			expectErr: dao.ErrGenerationNotHeld,
		},
		{
			name:               "Error/NotHeldByThisWorker",
			worker:             "someone-else",
			recordProviderCall: true,
			expectErr:          dao.ErrGenerationNotHeld,
		},
	}

	daoObserveLater := dao.NewGenerationObserveLater()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				seedGeneration(ctx, t, 3)
				claimed := claimGenerations(ctx, t)

				if testCase.recordProviderCall {
					_, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
						ID: claimed[0].ID, WorkerID: testWorker, ProviderCallID: "resp_1",
					})
					require.NoError(t, err)
				}

				before := time.Now()
				observedLater, err := daoObserveLater.Exec(ctx, &dao.GenerationObserveLaterRequest{
					ID: claimed[0].ID, WorkerID: testCase.worker, RetryAfter: time.Second,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					require.Nil(t, observedLater)

					return
				}

				require.Equal(t, dao.GenerationStatusPending, observedLater.Status)
				require.Nil(t, observedLater.ClaimedBy)
				require.Nil(t, observedLater.LeaseExpiresAt)
				require.Equal(t, "resp_1", *observedLater.ProviderCallID)
				require.False(t, observedLater.RunAt.Before(before.Add(time.Second)))
			})
		})
	}
}

func TestGenerationObserveLaterPreservesInferenceAttempt(t *testing.T) {
	t.Parallel()

	postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()

		seedGeneration(ctx, t, 1)
		claimed := claimGenerations(ctx, t)
		generation := claimed[0]

		_, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
			ID: generation.ID, WorkerID: testWorker, ProviderCallID: "resp_1",
		})
		require.NoError(t, err)

		for range 3 {
			_, err = dao.NewGenerationObserveLater().Exec(ctx, &dao.GenerationObserveLaterRequest{
				ID: generation.ID, WorkerID: testWorker,
			})
			require.NoError(t, err)

			resumed := claimGenerations(ctx, t)
			require.Len(t, resumed, 1)
			require.Equal(t, generation.Attempt, resumed[0].Attempt)
			require.NotNil(t, resumed[0].ProviderCallID)
			require.Equal(t, "resp_1", *resumed[0].ProviderCallID)
		}
	})
}
