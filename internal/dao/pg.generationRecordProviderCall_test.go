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

func TestGenerationRecordProviderCall(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// recordFirst is an identifier written before the one under test.
		recordFirst string
		// worker recording the call. A worker that does not hold the claim must be refused.
		worker string

		providerCallID string

		expect    string
		expectErr error
	}{
		{
			name: "Success",

			worker:         testWorker,
			providerCallID: "resp_1",

			expect: "resp_1",
		},
		{
			// Whoever started a replacement operation after the first stalled is the authority on
			// which identifier is live.
			name: "Success/OverwritesAPreviousIdentifier",

			recordFirst:    "resp_1",
			worker:         testWorker,
			providerCallID: "resp_2",

			expect: "resp_2",
		},
		{
			name: "Error/NotHeldByThisWorker",

			worker:         "someone-else",
			providerCallID: "resp_1",

			expectErr: dao.ErrGenerationNotHeld,
		},
	}

	daoRecord := dao.NewGenerationRecordProviderCall()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				seedGeneration(ctx, t, 1)
				claimed := claimGenerations(ctx, t)

				if testCase.recordFirst != "" {
					_, err := daoRecord.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
						ID: claimed[0].ID, WorkerID: testWorker, ProviderCallID: testCase.recordFirst,
					})
					require.NoError(t, err)
				}

				result, err := daoRecord.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
					ID: claimed[0].ID, WorkerID: testCase.worker, ProviderCallID: testCase.providerCallID,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					require.Nil(t, result)

					return
				}

				require.Equal(t, testCase.expect, *result.ProviderCallID)
			})
		})
	}
}
