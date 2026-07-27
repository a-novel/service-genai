package dao_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationReap(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// pending is how many generations are seeded and claimed.
		pending int
		// maxAttempts on each. One attempt means a lapsed lease is terminal.
		maxAttempts int16
		// leaseAge is how long ago the lease lapsed. Zero leaves it live.
		leaseAge time.Duration

		request *dao.GenerationReapRequest

		expectReaped int
		expectStatus dao.GenerationStatus
	}{
		{
			// No attempt left, so the lapse is terminal.
			name: "Success/AbandonsWithNoAttemptLeft",

			pending: 1, maxAttempts: 1, leaseAge: time.Hour,
			request: &dao.GenerationReapRequest{Grace: 0, Retention: testRetention, Limit: 10},

			expectReaped: 1,
			expectStatus: dao.GenerationStatusAbandoned,
		},
		{
			name: "Success/RequeuesWithAttemptsLeft",

			pending: 1, maxAttempts: 3, leaseAge: time.Hour,
			request: &dao.GenerationReapRequest{Grace: 0, Retention: testRetention, Limit: 10},

			expectReaped: 1,
			expectStatus: dao.GenerationStatusPending,
		},
		{
			// A worker that finished just past its lease must beat the sweep, or its work is run
			// twice and billed twice.
			name: "Success/GraceLeavesARecentlyLapsedLeaseAlone",

			pending: 1, maxAttempts: 1, leaseAge: time.Minute,
			request: &dao.GenerationReapRequest{Grace: time.Hour, Retention: testRetention, Limit: 10},

			expectReaped: 0,
		},
		{
			name: "Success/LeavesLiveLeasesAlone",

			pending: 1, maxAttempts: 1,
			request: &dao.GenerationReapRequest{Grace: 0, Retention: testRetention, Limit: 10},

			expectReaped: 0,
		},
		{
			// One sweep never materialises the whole stranded set; the caller repeats until a sweep
			// comes back short.
			name: "Success/LimitCapsOneSweep",

			pending: 3, maxAttempts: 1, leaseAge: time.Hour,
			request: &dao.GenerationReapRequest{Grace: 0, Retention: testRetention, Limit: 2},

			expectReaped: 2,
			expectStatus: dao.GenerationStatusAbandoned,
		},
	}

	daoReap := dao.NewGenerationReap()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				for range testCase.pending {
					seedGeneration(ctx, t, testCase.maxAttempts)
				}

				claimed := claimGenerations(ctx, t)

				// Recorded on every claim, so the preservation assertion below has something to
				// check: a crash must not cost the provider call already paid for.
				daoRecord := dao.NewGenerationRecordProviderCall()

				for _, generation := range claimed {
					_, err := daoRecord.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
						ID: generation.ID, WorkerID: testWorker, ProviderCallID: "resp_" + generation.ID.String(),
					})
					require.NoError(t, err)

					if testCase.leaseAge > 0 {
						expireLease(ctx, t, generation.ID, testCase.leaseAge)
					}
				}

				reaped, err := daoReap.Exec(ctx, testCase.request)
				require.NoError(t, err)
				require.Len(t, reaped, testCase.expectReaped)

				for _, generation := range reaped {
					require.Equal(t, testCase.expectStatus, generation.Status)
					require.Nil(t, generation.ClaimedBy)
					require.Nil(t, generation.LeaseExpiresAt)
					// Preserved, unlike a requeue: the worker died, the provider's operation did
					// not, so the next claim re-attaches instead of paying again.
					require.NotNil(t, generation.ProviderCallID)

					if testCase.expectStatus == dao.GenerationStatusAbandoned {
						require.NotNil(t, generation.SettledAt)
						require.NotNil(t, generation.ExpiresAt)
					}
				}
			})
		})
	}
}
