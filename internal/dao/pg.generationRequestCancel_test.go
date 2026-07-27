package dao_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationRequestCancel(t *testing.T) {
	t.Parallel()

	otherOwner := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	testCases := []struct {
		name string

		// claimFirst puts the generation in flight, which is still cancellable.
		claimFirst bool
		// settleFirst puts it past the point of stopping.
		settleFirst bool
		// requestTwice marks it a second time.
		requestTwice bool

		owner uuid.UUID

		expectErr error
	}{
		{
			name: "Success/Pending",

			owner: testOwner,
		},
		{
			name: "Success/Running",

			claimFirst: true,
			owner:      testOwner,
		},
		{
			// The timestamp is the moment the first request arrived, so a repeat does not push the
			// worker's view of when the stop was asked for.
			name: "Success/RepeatKeepsTheFirstTimestamp",

			requestTwice: true,
			owner:        testOwner,
		},
		{
			// Already paid for. Reporting this rather than silently succeeding is what tells a
			// caller its stop came too late.
			name: "Error/AlreadySettled",

			claimFirst:  true,
			settleFirst: true,
			owner:       testOwner,

			expectErr: dao.ErrGenerationNotCancellable,
		},
		{
			// Another owner's generation is indistinguishable from one that does not exist.
			name: "Error/OtherOwner",

			owner: otherOwner,

			expectErr: dao.ErrGenerationNotCancellable,
		},
	}

	daoRequestCancel := dao.NewGenerationRequestCancel()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 1)

				if testCase.claimFirst {
					claimGenerations(ctx, t)
				}

				if testCase.settleFirst {
					_, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
						ID: generation.ID, WorkerID: testWorker,
						Status: dao.GenerationStatusSucceeded, Retention: testRetention,
					})
					require.NoError(t, err)
				}

				var firstRequestedAt *time.Time

				if testCase.requestTwice {
					first, err := daoRequestCancel.Exec(ctx, &dao.GenerationRequestCancelRequest{
						ID: generation.ID, OwnerID: testCase.owner,
					})
					require.NoError(t, err)

					firstRequestedAt = first.CancelRequestedAt
				}

				result, err := daoRequestCancel.Exec(ctx, &dao.GenerationRequestCancelRequest{
					ID: generation.ID, OwnerID: testCase.owner,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					require.Nil(t, result)

					return
				}

				require.NotNil(t, result.CancelRequestedAt)

				if firstRequestedAt != nil {
					require.Equal(t, *firstRequestedAt, *result.CancelRequestedAt)
				}

				// Marking is all this does: the worker settles, because the tokens spent before the
				// stop still have to be recorded.
				require.NotEqual(t, dao.GenerationStatusCancelled, result.Status)
			})
		})
	}
}
