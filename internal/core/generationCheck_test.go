package core_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/transaction/transactiontest"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
	libmocks "github.com/a-novel/service-genai/internal/lib/mocks"
)

func TestGenerationCheck(t *testing.T) {
	t.Parallel()

	const retention = time.Hour

	callID := testCallID
	cancelledReason := "generation cancelled"
	unknownMessage := "the provider may have accepted the call, but its outcome is unknown"
	lostMessage := "the provider no longer holds the result"
	startFailedMessage := "the provider call could not be started"
	rejectedMessage := "the provider rejected the request"
	callFailedMessage := "the provider failed the call"
	unavailableMessage := "no adapter serves the generation's provider"

	rejectionBody := json.RawMessage(`{"error":{"message":"bad model"}}`)

	// settle describes the settle a case expects; the runner fills in the id and retention.
	type settle struct {
		attempt int16
		callID  *string
		status  dao.GenerationStatus
		reason  *string
	}

	testCases := []struct {
		name string

		generation *dao.Generation

		beginStartErr error
		start         *lib.ProviderCall
		startErr      error
		recordErr     error
		observe       *lib.ProviderCall
		observeErr    error
		settleErr     error
		attemptErr    error

		expectStart      bool
		expectRelease    bool
		expectRecord     bool
		expectStopOrphan bool
		expectSettle     *settle
		expectRequeue    bool
		// expectAttempt is the provider answer the attempt records, if any.
		expectAttempt json.RawMessage
		expectReread  bool
		// rereadAs is what the re-read finds, a settled generation unless stated.
		rereadAs *dao.Generation

		expectStatus dao.GenerationStatus
		expectErr    error
	}{
		{
			name: "Success/Settled",

			generation: settledGeneration(dao.GenerationStatusSucceeded),

			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			name: "Success/Start",

			generation: pendingGeneration(),
			start:      providerCall(lib.ProviderCallRunning),

			expectStart:  true,
			expectRecord: true,
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The provider refused before accepting anything, so the attempt costs nothing and comes back.
			name: "Success/StartRateLimited",

			generation: pendingGeneration(),
			startErr:   lib.ErrProviderRetryable,

			expectStart:   true,
			expectRelease: true,
			expectStatus:  dao.GenerationStatusPending,
		},
		{
			// Nothing ran. The caller built the request, so it reads the provider's reason to fix it.
			name: "Success/StartRejected",

			generation: pendingGeneration(),
			startErr:   &lib.ProviderRejectionError{Response: rejectionBody, Err: errFoo},

			expectStart:   true,
			expectSettle:  &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &rejectedMessage},
			expectAttempt: rejectionBody,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			name: "Success/StartRejectedWithoutBody",

			generation: pendingGeneration(),
			startErr:   &lib.ProviderRejectionError{Err: errFoo},

			expectStart:  true,
			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &rejectedMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/StartFailed",

			generation: pendingGeneration(),
			startErr:   errFoo,

			expectStart:  true,
			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &startFailedMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The call may have been accepted. Starting another could pay twice, so the attempt ends.
			name: "Success/StartAmbiguous",

			generation: pendingGeneration(),
			startErr:   lib.ErrProviderStartAmbiguous,

			expectStart:  true,
			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &unknownMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/StartedByAnotherCheck",

			generation:    pendingGeneration(),
			beginStartErr: dao.ErrGenerationChanged,

			expectReread: true,
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			// The generation settled while the start was in flight: the accepted call belongs to nothing.
			name: "Success/OrphanStopped",

			generation: pendingGeneration(),
			start:      providerCall(lib.ProviderCallRunning),
			recordErr:  dao.ErrGenerationChanged,

			expectStart:      true,
			expectRecord:     true,
			expectStopOrphan: true,
			expectReread:     true,
			expectStatus:     dao.GenerationStatusSucceeded,
		},
		{
			// A deployment dropped the provider's adapter: nothing can run the generation any more.
			name: "Success/ProviderUnavailable",

			generation: withProvider(pendingGeneration(), "gone"),

			expectSettle: &settle{status: dao.GenerationStatusFailed, reason: &unavailableMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/ProviderUnavailableWhileRunning",

			generation: withProvider(runningGeneration(1), "gone"),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &unavailableMessage,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/CancelledBeforeStart",

			generation: withCancel(pendingGeneration()),

			expectSettle: &settle{status: dao.GenerationStatusCancelled, reason: &cancelledReason},
			expectStatus: dao.GenerationStatusCancelled,
		},
		{
			// Another process may still be sending this start, so nothing happens yet.
			name: "Success/StartInFlight",

			generation: startingGeneration(10 * time.Second),

			expectStatus: dao.GenerationStatusPending,
		},
		{
			name: "Success/StartOutcomeUnknown",

			generation: startingGeneration(time.Minute),

			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &unknownMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/StillRunning",

			generation: runningGeneration(1),
			observe:    providerCall(lib.ProviderCallRunning),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			name: "Success/Succeeded",

			generation: runningGeneration(1),
			observe:    providerCall(lib.ProviderCallSucceeded),

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// The same answer recorded by an earlier settle that did not commit its status is kept once.
			name: "Success/AttemptAlreadyRecorded",

			generation: runningGeneration(1),
			observe:    providerCall(lib.ProviderCallSucceeded),
			attemptErr: dao.ErrGenerationAttemptExists,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A cancelled call is not a free one: its answer is recorded.
			name: "Success/Cancelled",

			generation: withCancel(runningGeneration(1)),
			observe:    providerCall(lib.ProviderCallCancelled),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			name: "Success/ProviderFailed",

			generation: runningGeneration(1),
			observe:    failedCall(false),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
			},
			expectAttempt: testResponse,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			name: "Success/RetryableFailure",

			generation: runningGeneration(1),
			observe:    failedCall(true),

			expectRequeue: true,
			expectAttempt: testResponse,
			expectStatus:  dao.GenerationStatusPending,
		},
		{
			name: "Success/RetriesExhausted",

			generation: runningGeneration(2),
			observe:    failedCall(true),

			expectSettle: &settle{
				attempt: 2, callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
			},
			expectAttempt: testResponse,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// The next check asks again; a provider hiccup is not the generation's failure.
			name: "Success/TransientObservationFailure",

			generation: runningGeneration(1),
			observeErr: lib.ErrProviderRetryable,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// Gone past the provider's retention window. Starting again would pay twice.
			name: "Success/CallLost",

			generation: runningGeneration(1),
			observeErr: errFoo,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &lostMessage,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/SettledByAnotherCheck",

			generation: runningGeneration(1),
			observe:    providerCall(lib.ProviderCallSucceeded),
			settleErr:  dao.ErrGenerationChanged,

			expectSettle: &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectReread: true,
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			// The write failed but landed: the generation carries the call, which must keep running.
			name: "Error/Record",

			generation: pendingGeneration(),
			start:      providerCall(lib.ProviderCallRunning),
			recordErr:  errFoo,

			expectStart:  true,
			expectRecord: true,
			expectReread: true,
			expectErr:    errFoo,
		},
		{
			// The write failed and never landed: nothing tracks the accepted call, so it is stopped.
			name: "Error/RecordLost",

			generation: pendingGeneration(),
			start:      providerCall(lib.ProviderCallRunning),
			recordErr:  errFoo,

			expectStart:      true,
			expectRecord:     true,
			expectReread:     true,
			rereadAs:         startingGeneration(time.Second),
			expectStopOrphan: true,
			expectErr:        errFoo,
		},
		{
			name: "Error/Settle",

			generation: runningGeneration(1),
			observe:    providerCall(lib.ProviderCallSucceeded),
			settleErr:  errFoo,

			expectSettle: &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectErr:    errFoo,
		},
		{
			// The settle and its answer commit together, or not at all.
			name: "Error/Attempt",

			generation: runningGeneration(1),
			observe:    providerCall(lib.ProviderCallSucceeded),
			attemptErr: errFoo,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectErr:     errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			generation := testCase.generation

			adapter := libmocks.NewMockAsyncAdapter(t)
			beginStart := coremocks.NewMockGenerationCheckBeginStartDao(t)
			releaseStart := coremocks.NewMockGenerationCheckReleaseStartDao(t)
			record := coremocks.NewMockGenerationCheckRecordProviderCallDao(t)
			settleDao := coremocks.NewMockGenerationCheckSettleDao(t)
			requeue := coremocks.NewMockGenerationCheckRequeueDao(t)
			attempt := coremocks.NewMockGenerationCheckAttemptInsertDao(t)
			get := coremocks.NewMockGenerationCheckGetDao(t)

			// The attempt a fresh start takes.
			intent := startingGeneration(0)

			if testCase.expectStart || testCase.beginStartErr != nil {
				beginStart.EXPECT().
					Exec(mock.Anything, &dao.GenerationBeginStartRequest{ID: generation.ID}).
					Return(intent, testCase.beginStartErr)
			}

			if testCase.expectStart {
				// The payload as the caller built it: the adapter writes its own envelope.
				adapter.EXPECT().
					Start(mock.Anything, &lib.AsyncStartRequest{Payload: testPayload}).
					Return(testCase.start, testCase.startErr)
			}

			if testCase.expectRelease {
				releaseStart.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationReleaseStartRequest) bool {
						return request.ID == generation.ID && request.Attempt == intent.Attempt && request.Delay > 0
					})).
					Return(pendingGeneration(), nil)
			}

			if testCase.expectRecord {
				record.EXPECT().
					Exec(mock.Anything, &dao.GenerationRecordProviderCallRequest{
						ID: generation.ID, Attempt: intent.Attempt, ProviderCallID: testCallID,
					}).
					Return(runningGeneration(1), testCase.recordErr)
			}

			if testCase.expectStopOrphan {
				adapter.EXPECT().Cancel(mock.Anything, testCallID).Return(providerCall(lib.ProviderCallCancelled), nil)
			}

			if testCase.observe != nil || testCase.observeErr != nil {
				if generation.CancelRequestedAt != nil {
					adapter.EXPECT().Cancel(mock.Anything, testCallID).Return(testCase.observe, testCase.observeErr)
				} else {
					adapter.EXPECT().Get(mock.Anything, testCallID).Return(testCase.observe, testCase.observeErr)
				}
			}

			if expected := testCase.expectSettle; expected != nil {
				// A settle is fenced on the start it read: the fresh intent's once one was taken here.
				started := generation.StartRequestedAt
				if testCase.expectStart {
					started = intent.StartRequestedAt
				}

				settleDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationSettleRequest{
						ID:               generation.ID,
						Attempt:          expected.attempt,
						StartRequestedAt: started,
						ProviderCallID:   expected.callID,
						Status:           expected.status,
						Error:            expected.reason,
						Retention:        retention,
					}).
					Return(settledGeneration(expected.status), testCase.settleErr)
			}

			if testCase.expectRequeue {
				requeue.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationRequeueRequest) bool {
						return request.ID == generation.ID && request.Attempt == generation.Attempt &&
							request.ProviderCallID == testCallID && request.Delay > 0
					})).
					Return(pendingGeneration(), nil)
			}

			if testCase.expectAttempt != nil {
				number := generation.Attempt
				if testCase.expectStart {
					number = intent.Attempt
				}

				attempt.EXPECT().
					Exec(mock.Anything, &dao.GenerationAttemptInsertRequest{
						GenerationID: generation.ID, Attempt: number, Response: testCase.expectAttempt,
					}).
					Return(&dao.GenerationAttempt{}, testCase.attemptErr)
			}

			if testCase.expectReread {
				get.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{ID: generation.ID, OwnerID: generation.OwnerID}).
					Return(lo.CoalesceOrEmpty(testCase.rereadAs, settledGeneration(dao.GenerationStatusSucceeded)), nil)
			}

			check, err := core.NewGenerationCheck(
				core.GenerationCheckConfig{Retention: retention},
				discardLogger{},
				lib.Adapters{testProvider: adapter},
				transactiontest.NewTransactor(),
				core.GenerationCheckDaos{
					BeginStart: beginStart, ReleaseStart: releaseStart, Record: record, Settle: settleDao,
					Requeue: requeue, Attempt: attempt, Get: get,
				},
			)
			require.NoError(t, err)

			result, err := check.Exec(t.Context(), &core.GenerationCheckRequest{Generation: generation})
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.expectStatus, result.Status)
			}

			for _, assertable := range []interface{ AssertExpectations(t mock.TestingT) bool }{
				adapter, beginStart, releaseStart, record, settleDao, requeue, attempt, get,
			} {
				assertable.AssertExpectations(t)
			}
		})
	}
}
