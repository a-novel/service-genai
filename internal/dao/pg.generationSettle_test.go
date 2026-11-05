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

func TestPgGenerationSettle(t *testing.T) {
	t.Parallel()

	failure := "the provider call failed"
	callID := testCallID
	otherCallID := "resp_other"

	testCases := []struct {
		name string

		// state is how far the seeded generation goes before the settle.
		state string
		// recordAfterRead lands the provider call id after the caller's read, as a concurrent start
		// would.
		recordAfterRead bool

		attempt int16
		callID  *string
		status  dao.GenerationStatus
		error   *string
		failure *dao.GenerationFailure
		// outcomeUnknown marks a failure whose start the provider may have accepted.
		outcomeUnknown bool

		expectErr error
	}{
		{
			name: "Success/Succeeded",

			state:   "running",
			attempt: 1,
			callID:  &callID,
			status:  dao.GenerationStatusSucceeded,
		},
		{
			// A start intent with no call id, past its timeout: the provider may have accepted it.
			name: "Success/OutcomeUnknown",

			state:          "starting",
			attempt:        1,
			status:         dao.GenerationStatusFailed,
			error:          &failure,
			failure:        new(dao.GenerationFailureOutcomeUnknown),
			outcomeUnknown: true,
		},
		{
			name: "Success/ProviderFailed",

			state:   "running",
			attempt: 1,
			callID:  &callID,
			status:  dao.GenerationStatusFailed,
			error:   &failure,
			failure: new(dao.GenerationFailureProviderFailed),
		},
		{
			// An earlier release left the generation pending between two starts.
			name: "Success/CancelledBeforeStart",

			state:  "pending",
			status: dao.GenerationStatusCancelled,
		},
		{
			name: "Error/OtherAttempt",

			state:   "running",
			attempt: 2,
			callID:  &callID,
			status:  dao.GenerationStatusSucceeded,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/OtherCall",

			state:   "running",
			attempt: 1,
			callID:  &otherCallID,
			status:  dao.GenerationStatusSucceeded,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// The check read a start with no id, but the id landed since. Settling as unknown would
			// discard a call that is now readable.
			name: "Error/RecordedSinceRead",

			state:           "starting",
			recordAfterRead: true,
			attempt:         1,
			status:          dao.GenerationStatusFailed,
			error:           &failure,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// Only a failure can leave a call running untracked: the values break a constraint, which no
			// retry changes.
			name: "Error/Refused",

			state:          "running",
			attempt:        1,
			callID:         &callID,
			status:         dao.GenerationStatusSucceeded,
			outcomeUnknown: true,

			expectErr: dao.ErrRefused,
		},
		{
			// Only a failure has a class.
			name: "Error/FailureClassOnSuccess",

			state:   "running",
			attempt: 1,
			callID:  &callID,
			status:  dao.GenerationStatusSucceeded,
			failure: new(dao.GenerationFailureProviderFailed),

			expectErr: dao.ErrRefused,
		},
		{
			name: "Error/AlreadySettled",

			state:   "settled",
			attempt: 1,
			callID:  &callID,
			status:  dao.GenerationStatusSucceeded,

			expectErr: dao.ErrGenerationChanged,
		},
	}

	daoSettle := dao.NewPgGenerationSettle()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				// A submitted generation is starting: its start intent is recorded.
				seed := seedGeneration
				if testCase.state == "pending" {
					seed = seedUnstarted
				}

				generation := seed(ctx, t)

				// read is the generation as the settling check last saw it.
				read := generation

				switch testCase.state {
				case "running":
					read = runGeneration(ctx, t, generation.ID)
				case "settled":
					read = settleGeneration(ctx, t, runGeneration(ctx, t, generation.ID))
				}

				if testCase.recordAfterRead {
					_, err := dao.NewPgGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
						ID: generation.ID, Attempt: 1, ProviderCallID: testCallID,
					})
					require.NoError(t, err)
				}

				settleRequest := &dao.GenerationSettleRequest{
					ID:               generation.ID,
					Attempt:          testCase.attempt,
					StartRequestedAt: read.StartRequestedAt,
					ProviderCallID:   testCase.callID,
					Status:           testCase.status,
					Error:            testCase.error,
					Failure:          testCase.failure,
					Retention:        testRetention,
					OutcomeUnknown:   testCase.outcomeUnknown,
				}

				settled, err := daoSettle.Exec(ctx, settleRequest)
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, testCase.status, settled.Status)
				require.Equal(t, testCase.error, settled.Error)
				require.Equal(t, testCase.failure, settled.Failure)
				require.NotNil(t, settled.SettledAt)
				require.NotNil(t, settled.ExpiresAt)
				require.Equal(t, testRetention, settled.ExpiresAt.Sub(*settled.SettledAt))

				// Marked at the settle's own instant, so admission's window starts there.
				if testCase.outcomeUnknown {
					require.Equal(t, settled.SettledAt, settled.OutcomeUnknownAt)
				} else {
					require.Nil(t, settled.OutcomeUnknownAt)
				}
			})
		})
	}
}
