package dao_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationRequestCancel(t *testing.T) {
	t.Parallel()

	const reason = "generation cancelled"

	testCases := []struct {
		name string

		// state is how far the seeded generation goes before the cancel.
		state string
		owner uuid.UUID
		// cancelTwice repeats the request, which must keep the first timestamp.
		cancelTwice bool

		expectStatus dao.GenerationStatus
		expectErr    error
	}{
		{
			// Nothing was sent, so there is no call to stop and the generation settles at once.
			name: "Success/NeverStarted",

			state: "pending",
			owner: testOwner,

			expectStatus: dao.GenerationStatusCancelled,
		},
		{
			// The start may be accepted at any moment, so the generation is only marked.
			name: "Success/StartInFlight",

			state: "starting",
			owner: testOwner,

			expectStatus: dao.GenerationStatusPending,
		},
		{
			name: "Success/Running",

			state: "running",
			owner: testOwner,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			name: "Success/Repeated",

			state:       "running",
			owner:       testOwner,
			cancelTwice: true,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			name: "Error/Settled",

			state: "settled",
			owner: testOwner,

			expectErr: dao.ErrGenerationNotCancellable,
		},
		{
			name: "Error/OtherOwner",

			state: "running",
			owner: uuid.MustParse("00000000-0000-0000-0000-000000000002"),

			expectErr: dao.ErrGenerationNotCancellable,
		},
	}

	daoRequestCancel := dao.NewGenerationRequestCancel()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 1)

				switch testCase.state {
				case "starting":
					startGeneration(ctx, t, generation.ID)
				case "running":
					runGeneration(ctx, t, generation.ID)
				case "settled":
					settleGeneration(ctx, t, runGeneration(ctx, t, generation.ID))
				}

				request := &dao.GenerationRequestCancelRequest{
					ID: generation.ID, OwnerID: testCase.owner, Error: reason, Retention: testRetention,
				}

				var first *dao.Generation

				if testCase.cancelTwice {
					var err error

					first, err = daoRequestCancel.Exec(ctx, request)
					require.NoError(t, err)
				}

				cancelled, err := daoRequestCancel.Exec(ctx, request)
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, testCase.expectStatus, cancelled.Status)
				require.NotNil(t, cancelled.CancelRequestedAt)

				if testCase.expectStatus == dao.GenerationStatusCancelled {
					require.Equal(t, reason, *cancelled.Error)
					require.NotNil(t, cancelled.SettledAt)
				} else {
					require.Nil(t, cancelled.SettledAt)
				}

				if first != nil {
					require.Equal(t, first.CancelRequestedAt, cancelled.CancelRequestedAt)
				}
			})
		})
	}
}
