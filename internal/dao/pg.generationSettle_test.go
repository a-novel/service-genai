package dao_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationSettle(t *testing.T) {
	t.Parallel()

	failure := "the provider call failed"
	kindFailed := "failed"
	callID := testCallID
	otherCallID := "resp_other"

	testCases := []struct {
		name string

		// state is how far the seeded generation goes before the settle.
		state string
		// recordAfterRead lands the provider call id after the caller's read, as a concurrent start
		// would.
		recordAfterRead bool
		// restartAfterRead restarts the generation on a newer epoch after the caller's read, which
		// keeps the attempt number and clears the start.
		restartAfterRead bool

		attempt int16
		callID  *string
		status  dao.GenerationStatus
		output  json.RawMessage
		failure *string
		error   *string

		expectErr error
		// expectRefused is a write the schema itself refuses.
		expectRefused bool
	}{
		{
			name: "Success/Succeeded",

			state:   "running",
			attempt: 1,
			callID:  &callID,
			status:  dao.GenerationStatusSucceeded,
			output:  json.RawMessage(`{"text": "done"}`),
		},
		{
			// A start intent with no call id, past its timeout: the provider may have accepted it.
			name: "Success/OutcomeUnknown",

			state:   "starting",
			attempt: 1,
			status:  dao.GenerationStatusFailed,
			failure: &kindFailed,
			error:   &failure,
		},
		{
			// A failed generation always says what kind of failure ended it.
			name: "Error/FailedWithoutKind",

			state:   "starting",
			attempt: 1,
			status:  dao.GenerationStatusFailed,
			error:   &failure,

			expectRefused: true,
		},
		{
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
			failure:         &kindFailed,
			error:           &failure,

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// A check read a start in flight, then a newer epoch restarted the generation. Settling the
			// read as unknown would fail a generation that is about to start on the new provider.
			name: "Error/RestartedSinceRead",

			state:            "starting",
			restartAfterRead: true,
			attempt:          1,
			status:           dao.GenerationStatusFailed,
			failure:          &kindFailed,
			error:            &failure,

			expectErr: dao.ErrGenerationChanged,
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

	daoSettle := dao.NewGenerationSettle()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 1)

				// read is the generation as the settling check last saw it.
				read := generation

				switch testCase.state {
				case "starting":
					read = startGeneration(ctx, t, generation.ID)
				case "running":
					read = runGeneration(ctx, t, generation.ID)
				case "settled":
					read = settleGeneration(ctx, t, runGeneration(ctx, t, generation.ID))
				}

				if testCase.recordAfterRead {
					_, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
						ID: generation.ID, Attempt: 1, ProviderCallID: testCallID,
					})
					require.NoError(t, err)
				}

				if testCase.restartAfterRead {
					_, err := dao.NewGenerationRestart().Exec(ctx, &dao.GenerationRestartRequest{
						ID: generation.ID, Attempt: 1, ProviderEpoch: testEpoch + 1,
					})
					require.NoError(t, err)
				}

				settled, err := daoSettle.Exec(ctx, &dao.GenerationSettleRequest{
					ID:               generation.ID,
					Attempt:          testCase.attempt,
					StartRequestedAt: read.StartRequestedAt,
					ProviderCallID:   testCase.callID,
					Status:           testCase.status,
					Output:           testCase.output,
					Failure:          testCase.failure,
					Error:            testCase.error,
					Retention:        testRetention,
				})
				if testCase.expectRefused {
					require.Error(t, err)
					require.NotErrorIs(t, err, dao.ErrGenerationChanged)

					return
				}

				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, testCase.status, settled.Status)
				require.Equal(t, testCase.failure, settled.Failure)
				require.Equal(t, testCase.error, settled.Error)
				require.NotNil(t, settled.SettledAt)
				require.NotNil(t, settled.ExpiresAt)
				require.Equal(t, testRetention, settled.ExpiresAt.Sub(*settled.SettledAt))

				if testCase.output != nil {
					require.JSONEq(t, string(testCase.output), string(settled.Output))
				}
			})
		})
	}
}
