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

func TestGenerationClaim(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// pending is how many generations wait before the claim.
		pending int
		// claimedFirst takes work away before the claim under test runs.
		claimedFirst bool

		request *dao.GenerationClaimRequest

		expectClaimed int
	}{
		{
			name: "Success",

			pending: 1,
			request: &dao.GenerationClaimRequest{WorkerID: testWorker, Limit: 10, Lease: testLease},

			expectClaimed: 1,
		},
		{
			name: "Success/EmptyQueue",

			request: &dao.GenerationClaimRequest{WorkerID: testWorker, Limit: 10, Lease: testLease},

			expectClaimed: 0,
		},
		{
			name: "Success/LimitCapsTheBatch",

			pending: 3,
			request: &dao.GenerationClaimRequest{WorkerID: testWorker, Limit: 2, Lease: testLease},

			expectClaimed: 2,
		},
		{
			// Concurrent claims take disjoint batches, so a second worker finds nothing rather than
			// running the same generation twice.
			name: "Success/AlreadyClaimedWorkIsNotTakenTwice",

			pending:      1,
			claimedFirst: true,
			request:      &dao.GenerationClaimRequest{WorkerID: "worker-2", Limit: 10, Lease: testLease},

			expectClaimed: 0,
		},
	}

	daoGenerationClaim := dao.NewGenerationClaim()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				for range testCase.pending {
					seedGeneration(ctx, t, 1)
				}

				if testCase.claimedFirst {
					claimGenerations(ctx, t)
				}

				claimed, err := daoGenerationClaim.Exec(ctx, testCase.request)
				require.NoError(t, err)
				require.Len(t, claimed, testCase.expectClaimed)

				for _, generation := range claimed {
					require.Equal(t, dao.GenerationStatusRunning, generation.Status)
					require.Equal(t, int16(1), generation.Attempt)
					require.Equal(t, testCase.request.WorkerID, *generation.ClaimedBy)
					// The lease comes off the database clock, so it is always in the future here
					// regardless of the test machine's own clock.
					require.NotNil(t, generation.LeaseExpiresAt)
					require.True(t, generation.LeaseExpiresAt.After(time.Now().Add(-time.Minute)))
				}
			})
		})
	}
}
