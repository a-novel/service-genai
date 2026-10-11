package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/a-novel-kit/golib/transaction/transactiontest"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
	connectorsmocks "github.com/a-novel/service-genai/internal/lib/connectors/mocks"
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
	unavailableMessage := "no connector serves the generation's provider"

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
		start         *connectors.Call
		startErr      error
		recordErr     error
		observe       *connectors.Call
		observeErr    error
		settleErr     error
		attemptErr    error
		usageErr      error
		// callerGone runs the check for a caller whose call already ended.
		callerGone bool

		expectStart      bool
		expectRelease    bool
		expectRecord     bool
		expectStopOrphan bool
		expectSettle     *settle
		expectRequeue    bool
		// expectAttempt is the provider answer the attempt records, if any.
		expectAttempt json.RawMessage
		// expectUsage records what the observed call consumed.
		expectUsage  bool
		expectReread bool
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
			start:      providerCall(connectors.CallRunning),

			expectStart:  true,
			expectRecord: true,
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The provider refused before accepting anything, so the attempt costs nothing and comes back.
			name: "Success/StartRateLimited",

			generation: pendingGeneration(),
			startErr:   connectors.ErrRetryable,

			expectStart:   true,
			expectRelease: true,
			expectStatus:  dao.GenerationStatusPending,
		},
		{
			// A request the provider keeps turning away must not hold a concurrency slot forever.
			name: "Success/StartRetriesExhausted",

			generation: submittedAgo(pendingGeneration(), time.Hour),
			startErr:   connectors.ErrRetryable,

			expectStart:  true,
			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &startFailedMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// Nothing ran. The caller built the request, so it reads the provider's reason to fix it.
			name: "Success/StartRejected",

			generation: pendingGeneration(),
			startErr:   &connectors.RejectionError{Response: rejectionBody, Err: errFoo},

			expectStart:   true,
			expectSettle:  &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &rejectedMessage},
			expectAttempt: rejectionBody,
			expectStatus:  dao.GenerationStatusFailed,
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
			startErr:   connectors.ErrStartAmbiguous,

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
			start:      providerCall(connectors.CallRunning),
			recordErr:  dao.ErrGenerationChanged,

			expectStart:      true,
			expectRecord:     true,
			expectStopOrphan: true,
			expectReread:     true,
			expectStatus:     dao.GenerationStatusSucceeded,
		},
		{
			// A deployment dropped the provider's connector: nothing can run the generation any more.
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
			observe:    providerCall(connectors.CallRunning),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			name: "Success/Succeeded",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// The same answer recorded by an earlier settle that did not commit its status is kept once.
			name: "Success/AttemptAlreadyRecorded",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			attemptErr: dao.ErrGenerationAttemptExists,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A cancelled call is not a free one: its answer is recorded.
			name: "Success/Cancelled",

			generation: withCancel(runningGeneration(1)),
			observe:    providerCall(connectors.CallCancelled),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
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
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			name: "Success/RetryableFailure",

			generation: runningGeneration(1),
			observe:    failedCall(true),

			expectRequeue: true,
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusPending,
		},
		{
			// The owner asked to stop: a fresh attempt would run what they cancelled.
			name: "Success/RetryableFailureAfterCancel",

			generation: withCancel(runningGeneration(1)),
			observe:    failedCall(true),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// A poll that elected the check stamped the generation: a check cut by the caller's
			// deadline would leave it stale, so it runs to the end on its own.
			name: "Success/CallerGone",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			callerGone: true,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			name: "Success/RetriesExhausted",

			generation: runningGeneration(2),
			observe:    failedCall(true),

			expectSettle: &settle{
				attempt: 2, callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// The next check asks again; a provider hiccup is not the generation's failure.
			name: "Success/TransientObservationFailure",

			generation: runningGeneration(1),
			observeErr: connectors.ErrRetryable,

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
			observe:    providerCall(connectors.CallSucceeded),
			settleErr:  dao.ErrGenerationChanged,

			expectSettle: &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectReread: true,
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			// The write failed but landed: the generation carries the call, which must keep running.
			name: "Error/Record",

			generation: pendingGeneration(),
			start:      providerCall(connectors.CallRunning),
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
			start:      providerCall(connectors.CallRunning),
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
			observe:    providerCall(connectors.CallSucceeded),
			settleErr:  errFoo,

			expectSettle: &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectErr:    errFoo,
		},
		{
			// The same usage recorded by an earlier settle that did not commit its status is counted once.
			name: "Success/UsageAlreadyRecorded",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			usageErr:   dao.ErrGenerationUsageExists,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A billed call nothing counts would leak past every cap: the settle waits for its usage.
			name: "Error/Usage",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			usageErr:   errFoo,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectErr:     errFoo,
		},
		{
			// The settle and its answer commit together, or not at all.
			name: "Error/Attempt",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
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

			connector := connectorsmocks.NewMockAsync(t)
			beginStart := coremocks.NewMockGenerationCheckDaoBeginStart(t)
			releaseStart := coremocks.NewMockGenerationCheckDaoReleaseStart(t)
			record := coremocks.NewMockGenerationCheckDaoRecordProviderCall(t)
			settleDao := coremocks.NewMockGenerationCheckDaoSettle(t)
			requeue := coremocks.NewMockGenerationCheckDaoRequeue(t)
			attempt := coremocks.NewMockGenerationCheckDaoAttemptInsert(t)
			usage := coremocks.NewMockGenerationCheckDaoUsageInsert(t)
			get := coremocks.NewMockGenerationCheckDaoGet(t)

			// The attempt a fresh start takes.
			intent := startingGeneration(0)
			intent.CreatedAt = generation.CreatedAt

			// usageOf is the Usage Record of a call answered for an attempt. The test provider reports
			// no start, so the call is timed from when it was sent.
			usageOf := func(attempt *dao.Generation, call *connectors.Call) *dao.GenerationUsageInsertRequest {
				return &dao.GenerationUsageInsertRequest{
					GenerationID:      attempt.ID,
					Attempt:           attempt.Attempt,
					OwnerID:           attempt.OwnerID,
					Purpose:           attempt.Purpose,
					Provider:          attempt.Provider,
					Model:             call.Model,
					Class:             call.Class,
					StartedAt:         *attempt.StartRequestedAt,
					InputTokens:       call.Usage.InputTokens,
					CachedInputTokens: call.Usage.CachedInputTokens,
					OutputTokens:      call.Usage.OutputTokens,
					ReasoningTokens:   call.Usage.ReasoningTokens,
				}
			}

			if testCase.expectStart || testCase.beginStartErr != nil {
				beginStart.EXPECT().
					Exec(mock.Anything, &dao.GenerationBeginStartRequest{ID: generation.ID}).
					Return(intent, testCase.beginStartErr)
			}

			if testCase.expectStart {
				// The payload as the caller built it: the connector writes its own envelope.
				connector.EXPECT().
					Start(mock.Anything, &connectors.AsyncStartRequest{Payload: testPayload}).
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
				orphan := providerCall(connectors.CallCancelled)

				connector.EXPECT().Cancel(mock.Anything, testCallID).Return(orphan, nil)
				// The orphan ran on the provider's side: what it consumed still counts.
				usage.EXPECT().Exec(mock.Anything, usageOf(intent, orphan)).Return(&dao.GenerationUsage{}, nil)
			}

			if testCase.observe != nil || testCase.observeErr != nil {
				if generation.CancelRequestedAt != nil {
					connector.EXPECT().Cancel(mock.Anything, testCallID).Return(testCase.observe, testCase.observeErr)
				} else {
					// The read must run on a live context, whatever became of the caller's.
					connector.EXPECT().Get(mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil }), testCallID).
						Return(testCase.observe, testCase.observeErr)
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

			if testCase.expectUsage {
				usage.EXPECT().
					Exec(mock.Anything, usageOf(generation, testCase.observe)).
					Return(&dao.GenerationUsage{}, testCase.usageErr)
			}

			if testCase.expectReread {
				get.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{ID: generation.ID, OwnerID: generation.OwnerID}).
					Return(lo.CoalesceOrEmpty(testCase.rereadAs, settledGeneration(dao.GenerationStatusSucceeded)), nil)
			}

			logger := coremocks.NewMockGenerationCheckLogger(t)
			// Provider details stay server-side, out of assertions.
			logger.EXPECT().Err(mock.Anything, mock.Anything).Maybe()

			check := core.NewGenerationCheck(
				core.GenerationCheckConfig{Retention: retention},
				logger,
				connectors.Registry{testProvider: connector},
				transactiontest.NewTransactor(),
				core.GenerationCheckDaos{
					BeginStart: beginStart, ReleaseStart: releaseStart, Record: record, Settle: settleDao,
					Requeue: requeue, Attempt: attempt, Usage: usage, Get: get,
				},
			)

			ctx := t.Context()

			if testCase.callerGone {
				var cancelCaller context.CancelFunc

				ctx, cancelCaller = context.WithCancel(ctx)
				cancelCaller()
			}

			result, err := check.Exec(ctx, &core.GenerationCheckRequest{Generation: generation})
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.expectStatus, result.Status)
			}

			for _, assertable := range []interface{ AssertExpectations(t mock.TestingT) bool }{
				connector, beginStart, releaseStart, record, settleDao, requeue, attempt, usage, get,
			} {
				assertable.AssertExpectations(t)
			}
		})
	}
}

//nolint:paralleltest // Replaces the process-wide tracer provider while checks run.
func TestGenerationCheckTelemetry(t *testing.T) {
	submitter := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	inCallerTrace := fmt.Sprintf("00-%s-%s-01", testCaller.TraceID(), trace.SpanID{2})

	testCases := []struct {
		name string

		traceparent *string

		expectLink *string
	}{
		{
			// The sweep, or a poll from another request, links back to the submission.
			name: "Success/LinksTheSubmittingRequest",

			traceparent: &submitter,

			expectLink: new("0af7651916cd43dd8448eb211c80319c"),
		},
		{
			// The check already runs in the submitting trace, so a link would point at itself.
			name: "Success/SameTrace",

			traceparent: &inCallerTrace,
		},
		{
			name: "Success/SubmittedOutsideATrace",
		},
		{
			name: "Success/UnreadableTraceContext",

			traceparent: new("not-a-traceparent"),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := otel.GetTracerProvider()

			otel.SetTracerProvider(provider)
			t.Cleanup(func() {
				otel.SetTracerProvider(previous)
				require.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context())))
			})

			check := core.NewGenerationCheck(
				core.GenerationCheckConfig{Retention: time.Hour},
				coremocks.NewMockGenerationCheckLogger(t),
				connectors.Registry{testProvider: connectorsmocks.NewMockAsync(t)},
				transactiontest.NewTransactor(),
				core.GenerationCheckDaos{
					BeginStart:   coremocks.NewMockGenerationCheckDaoBeginStart(t),
					ReleaseStart: coremocks.NewMockGenerationCheckDaoReleaseStart(t),
					Record:       coremocks.NewMockGenerationCheckDaoRecordProviderCall(t),
					Settle:       coremocks.NewMockGenerationCheckDaoSettle(t),
					Requeue:      coremocks.NewMockGenerationCheckDaoRequeue(t),
					Attempt:      coremocks.NewMockGenerationCheckDaoAttemptInsert(t),
					Usage:        coremocks.NewMockGenerationCheckDaoUsageInsert(t),
					Get:          coremocks.NewMockGenerationCheckDaoGet(t),
				},
			)

			generation := settledGeneration(dao.GenerationStatusSucceeded)
			generation.Traceparent = testCase.traceparent

			_, err := check.Exec(
				trace.ContextWithSpanContext(t.Context(), testCaller), &core.GenerationCheckRequest{Generation: generation},
			)
			require.NoError(t, err)

			spans := recorder.Ended()
			require.Len(t, spans, 1)
			require.Equal(t, "core.GenerationCheck", spans[0].Name())

			if testCase.expectLink == nil {
				require.Empty(t, spans[0].Links())

				return
			}

			require.Len(t, spans[0].Links(), 1)
			require.Equal(t, *testCase.expectLink, spans[0].Links()[0].SpanContext.TraceID().String())
			require.True(t, spans[0].Links()[0].SpanContext.IsRemote())
		})
	}
}
