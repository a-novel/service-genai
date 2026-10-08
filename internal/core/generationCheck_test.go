package core_test

import (
	"encoding/json"
	"testing"
	"time"

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
	kindFailed := string(lib.FailureFailed)
	kindIncomplete := string(lib.FailureIncomplete)
	kindInvalid := string(lib.FailureInvalidRequest)
	unknownMessage := "the provider may have accepted the call, but its outcome is unknown"
	lostMessage := "the provider no longer holds the result"
	startFailedMessage := "the provider call could not be started"
	rejectedMessage := "the provider rejected the request"
	unreadableMessage := "the stored request is unreadable"
	incompleteMessage := "the output reached the tier's output token ceiling"
	callFailedMessage := "the provider failed to complete the call"

	succeeded := providerCall(lib.ProviderCallSucceeded)
	succeeded.Output = json.RawMessage(`{"text": "done"}`)

	retryable := failedCall(lib.Failure{Kind: lib.FailureFailed, Message: callFailedMessage})
	retryable.Retryable = true

	unreadable := pendingGeneration()
	unreadable.Request = json.RawMessage(`{"tier": 1}`)

	// settle describes the settle a case expects; the runner fills in the id and retention.
	type settle struct {
		attempt int16
		callID  *string
		status  dao.GenerationStatus
		output  json.RawMessage
		failure *string
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

		expectStart      bool
		expectRelease    bool
		expectRecord     bool
		expectStopOrphan bool
		expectSettle     *settle
		expectRequeue    bool
		expectUsage      bool
		expectReread     bool

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
			// Nothing ran, so the caller learns why and decides what to fix.
			name: "Success/StartRejected",

			generation: pendingGeneration(),
			startErr: &lib.ProviderRejectionError{
				Failure: lib.Failure{Kind: lib.FailureInvalidRequest, Message: rejectedMessage}, Err: errFoo,
			},

			expectStart: true,
			expectSettle: &settle{
				attempt: 1, status: dao.GenerationStatusFailed, failure: &kindInvalid, reason: &rejectedMessage,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/StartFailed",

			generation: pendingGeneration(),
			startErr:   errFoo,

			expectStart: true,
			expectSettle: &settle{
				attempt: 1, status: dao.GenerationStatusFailed, failure: &kindFailed, reason: &startFailedMessage,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/UnreadableRequest",

			generation: unreadable,

			expectSettle: &settle{status: dao.GenerationStatusFailed, failure: &kindFailed, reason: &unreadableMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The call may have been accepted. Starting another could pay twice, so the attempt ends.
			name: "Success/StartAmbiguous",

			generation: pendingGeneration(),
			startErr:   lib.ErrProviderStartAmbiguous,

			expectStart: true,
			expectSettle: &settle{
				attempt: 1, status: dao.GenerationStatusFailed, failure: &kindFailed, reason: &unknownMessage,
			},
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

			expectSettle: &settle{
				attempt: 1, status: dao.GenerationStatusFailed, failure: &kindFailed, reason: &unknownMessage,
			},
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
			observe:    succeeded,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded, output: succeeded.Output,
			},
			expectUsage:  true,
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			// A cancelled call is not a free one: what it consumed is still recorded.
			name: "Success/Cancelled",

			generation: withCancel(runningGeneration(1)),
			observe:    providerCall(lib.ProviderCallCancelled),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectUsage:  true,
			expectStatus: dao.GenerationStatusCancelled,
		},
		{
			name: "Success/Incomplete",

			generation: runningGeneration(1),
			observe:    failedCall(lib.Failure{Kind: lib.FailureIncomplete, Message: incompleteMessage}),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed,
				failure: &kindIncomplete, reason: &incompleteMessage,
			},
			expectUsage:  true,
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/RetryableFailure",

			generation: runningGeneration(1),
			observe:    retryable,

			expectRequeue: true,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusPending,
		},
		{
			name: "Success/RetriesExhausted",

			generation: runningGeneration(2),
			observe:    retryable,

			expectSettle: &settle{
				attempt: 2, callID: &callID, status: dao.GenerationStatusFailed,
				failure: &kindFailed, reason: &callFailedMessage,
			},
			expectUsage:  true,
			expectStatus: dao.GenerationStatusFailed,
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
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed,
				failure: &kindFailed, reason: &lostMessage,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/SettledByAnotherCheck",

			generation: runningGeneration(1),
			observe:    succeeded,
			settleErr:  dao.ErrGenerationChanged,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded, output: succeeded.Output,
			},
			expectReread: true,
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			name: "Error/Record",

			generation: pendingGeneration(),
			start:      providerCall(lib.ProviderCallRunning),
			recordErr:  errFoo,

			expectStart:  true,
			expectRecord: true,
			expectErr:    errFoo,
		},
		{
			name: "Error/Settle",

			generation: runningGeneration(1),
			observe:    succeeded,
			settleErr:  errFoo,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded, output: succeeded.Output,
			},
			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			generation := testCase.generation

			provider := libmocks.NewMockProvider(t)
			beginStart := coremocks.NewMockGenerationCheckBeginStartDao(t)
			releaseStart := coremocks.NewMockGenerationCheckReleaseStartDao(t)
			record := coremocks.NewMockGenerationCheckRecordProviderCallDao(t)
			settleDao := coremocks.NewMockGenerationCheckSettleDao(t)
			requeue := coremocks.NewMockGenerationCheckRequeueDao(t)
			usage := coremocks.NewMockGenerationCheckUsageInsertDao(t)
			get := coremocks.NewMockGenerationCheckGetDao(t)

			provider.EXPECT().Name().Return(lib.ProviderNameOpenAI).Maybe()

			// The attempt a fresh start takes.
			intent := startingGeneration(0)

			if testCase.expectStart || testCase.beginStartErr != nil {
				beginStart.EXPECT().
					Exec(mock.Anything, &dao.GenerationBeginStartRequest{ID: generation.ID}).
					Return(intent, testCase.beginStartErr)
			}

			if testCase.expectStart {
				provider.EXPECT().
					Start(mock.Anything, &lib.ProviderStartRequest{
						Tier:         lib.TierBalanced,
						Instructions: "Continue.",
						Input:        json.RawMessage(`{"scene":"a door"}`),
						OutputSchema: json.RawMessage(`{"type":"object"}`),
						EndUserID:    intent.OwnerID.String(),
						GenerationID: intent.ID.String(),
						Attempt:      intent.Attempt,
					}).
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
				provider.EXPECT().Cancel(mock.Anything, testCallID).Return(providerCall(lib.ProviderCallCancelled), nil)
			}

			if testCase.observe != nil || testCase.observeErr != nil {
				if generation.CancelRequestedAt != nil {
					provider.EXPECT().Cancel(mock.Anything, testCallID).Return(testCase.observe, testCase.observeErr)
				} else {
					provider.EXPECT().Get(mock.Anything, testCallID).Return(testCase.observe, testCase.observeErr)
				}
			}

			if expected := testCase.expectSettle; expected != nil {
				settleDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationSettleRequest{
						ID:             generation.ID,
						Attempt:        expected.attempt,
						ProviderCallID: expected.callID,
						Status:         expected.status,
						Output:         expected.output,
						Failure:        expected.failure,
						Error:          expected.reason,
						Retention:      retention,
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

			if testCase.expectUsage {
				usage.EXPECT().
					Exec(mock.Anything, &dao.GenerationUsageInsertRequest{
						GenerationID:      generation.ID,
						Attempt:           generation.Attempt,
						OwnerID:           generation.OwnerID,
						Purpose:           generation.Purpose,
						Provider:          lib.ProviderNameOpenAI,
						Model:             testCase.observe.Model,
						ReasoningEffort:   &testCase.observe.ReasoningEffort,
						InputTokens:       testCase.observe.Usage.InputTokens,
						CachedInputTokens: testCase.observe.Usage.CachedInputTokens,
						OutputTokens:      testCase.observe.Usage.OutputTokens,
						ReasoningTokens:   testCase.observe.Usage.ReasoningTokens,
					}).
					Return(&dao.GenerationUsage{}, nil)
			}

			if testCase.expectReread {
				get.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{ID: generation.ID, OwnerID: generation.OwnerID}).
					Return(settledGeneration(dao.GenerationStatusSucceeded), nil)
			}

			check, err := core.NewGenerationCheck(
				core.GenerationCheckConfig{Retention: retention},
				discardLogger{},
				provider,
				transactiontest.NewTransactor(),
				core.GenerationCheckDaos{
					BeginStart: beginStart, ReleaseStart: releaseStart, Record: record, Settle: settleDao,
					Requeue: requeue, Usage: usage, Get: get,
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
				provider, beginStart, releaseStart, record, settleDao, requeue, usage, get,
			} {
				assertable.AssertExpectations(t)
			}
		})
	}
}
