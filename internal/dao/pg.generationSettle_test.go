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

		// otherStart fences the settle on a start the generation does not carry.
		otherStart bool
		callID     *string
		status     dao.GenerationStatus
		error      *string
		failure    *dao.GenerationFailure
		// outcomeUnknown marks a failure whose start the provider may have accepted.
		outcomeUnknown bool

		expectErr error
	}{
		{
			name: "Success/Succeeded",

			state:  "running",
			callID: &callID,
			status: dao.GenerationStatusSucceeded,
		},
		{
			// A start intent with no call id, past its timeout: the provider may have accepted it.
			name: "Success/OutcomeUnknown",

			state:          "starting",
			status:         dao.GenerationStatusFailed,
			error:          &failure,
			failure:        new(dao.GenerationFailureOutcomeUnknown),
			outcomeUnknown: true,
		},
		{
			name: "Success/ProviderFailed",

			state:   "running",
			callID:  &callID,
			status:  dao.GenerationStatusFailed,
			error:   &failure,
			failure: new(dao.GenerationFailureProviderFailed),
		},
		{
			name: "Error/OtherStart",

			state:      "running",
			otherStart: true,
			callID:     &callID,
			status:     dao.GenerationStatusSucceeded,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/OtherCall",

			state:  "running",
			callID: &otherCallID,
			status: dao.GenerationStatusSucceeded,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// The check read a start with no id, but the id landed since. Settling as unknown would
			// discard a call that is now readable.
			name: "Error/RecordedSinceRead",

			state:           "starting",
			recordAfterRead: true,
			status:          dao.GenerationStatusFailed,
			error:           &failure,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// Only a failure can leave a call running untracked: the values break a constraint, which no
			// retry changes.
			name: "Error/Refused",

			state:          "running",
			callID:         &callID,
			status:         dao.GenerationStatusSucceeded,
			outcomeUnknown: true,

			expectErr: dao.ErrRefused,
		},
		{
			// Only a failure has a class.
			name: "Error/FailureClassOnSuccess",

			state:   "running",
			callID:  &callID,
			status:  dao.GenerationStatusSucceeded,
			failure: new(dao.GenerationFailureProviderFailed),

			expectErr: dao.ErrRefused,
		},
		{
			// The settled row carries the start and no call, as the settle does: only the status fence
			// refuses it.
			name: "Error/AlreadySettled",

			state:  "settled",
			status: dao.GenerationStatusSucceeded,

			expectErr: dao.ErrGenerationChanged,
		},
	}

	daoSettle := dao.NewPgGenerationSettle()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				// read is the generation as the settling check last saw it.
				read := seedState(ctx, t, testCase.state)

				if testCase.recordAfterRead {
					runGeneration(ctx, t, read.ID)
				}

				start := read.StartRequestedAt
				if testCase.otherStart {
					start = start.Add(-time.Minute)
				}

				settleRequest := &dao.GenerationSettleRequest{
					ID:               read.ID,
					StartRequestedAt: start,
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
