package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/transaction/transactiontest"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
	libmocks "github.com/a-novel/service-genai/internal/lib/mocks"
)

var errFoo = errors.New("foo")

const (
	testWorkerID = "worker-1"
	testCallID   = "resp_1"
)

// testResumeID stands in for an identifier a previous run already recorded.
var testResumeID = "resp_resumed"

func testWorkerConfig() core.WorkerConfig {
	return core.WorkerConfig{
		ID:           testWorkerID,
		Lease:        time.Minute,
		BatchSize:    10,
		PollInterval: time.Millisecond,
		Retention:    7 * 24 * time.Hour,
	}
}

// workerMocks is every dependency a worker takes, kept together so a case scripts one and asserts
// on the rest without rebuilding the set.
type workerMocks struct {
	provider *libmocks.MockProvider
	claim    *coremocks.MockWorkerClaimDao
	record   *coremocks.MockWorkerRecordProviderCallDao
	settle   *coremocks.MockWorkerSettleDao
	requeue  *coremocks.MockWorkerRequeueDao
	usage    *coremocks.MockWorkerUsageInsertDao
}

func newWorkerMocks(t *testing.T) *workerMocks {
	t.Helper()

	return &workerMocks{
		provider: libmocks.NewMockProvider(t),
		claim:    coremocks.NewMockWorkerClaimDao(t),
		record:   coremocks.NewMockWorkerRecordProviderCallDao(t),
		settle:   coremocks.NewMockWorkerSettleDao(t),
		requeue:  coremocks.NewMockWorkerRequeueDao(t),
		usage:    coremocks.NewMockWorkerUsageInsertDao(t),
	}
}

func (mocks *workerMocks) daos() core.WorkerDaos {
	return core.WorkerDaos{
		Claim: mocks.claim, Record: mocks.record, Settle: mocks.settle,
		Requeue: mocks.requeue, Usage: mocks.usage,
	}
}

func (mocks *workerMocks) worker(t *testing.T) *core.Worker {
	t.Helper()

	worker, err := core.NewWorker(
		testWorkerConfig(), mocks.provider, transactiontest.NewTransactor(), mocks.daos(),
	)
	if err != nil {
		panic(err)
	}

	return worker
}

func (mocks *workerMocks) assertExpectations(t *testing.T) {
	t.Helper()

	mocks.provider.AssertExpectations(t)
	mocks.claim.AssertExpectations(t)
	mocks.record.AssertExpectations(t)
	mocks.settle.AssertExpectations(t)
	mocks.requeue.AssertExpectations(t)
	mocks.usage.AssertExpectations(t)
}

func claimedGeneration(providerCallID *string, cancelRequested bool, maxAttempts int16) *dao.Generation {
	generation := &dao.Generation{
		ID:             uuid.MustParse("01999999-0000-7000-8000-000000000001"),
		OwnerID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Purpose:        "studio.generation",
		Request:        json.RawMessage(`{"model": "a-model"}`),
		Status:         dao.GenerationStatusRunning,
		Attempt:        1,
		MaxAttempts:    maxAttempts,
		ProviderCallID: providerCallID,
	}

	if cancelRequested {
		now := time.Now()
		generation.CancelRequestedAt = &now
	}

	return generation
}

func call(state lib.ProviderCallState) *lib.ProviderCall {
	return &lib.ProviderCall{ID: testCallID, State: state, Model: "a-model-snapshot"}
}

func succeededCall() *lib.ProviderCall {
	succeeded := call(lib.ProviderCallSucceeded)
	succeeded.Output = json.RawMessage(`{"text": "done"}`)
	succeeded.Usage = &lib.ProviderUsage{
		InputTokens: 1000, CachedInputTokens: 200, OutputTokens: 500, ReasoningTokens: 100,
	}

	return succeeded
}

func TestWorker(t *testing.T) {
	t.Parallel()

	incomplete := call(lib.ProviderCallIncomplete)
	incomplete.Reason = "max_output_tokens"
	incomplete.Usage = &lib.ProviderUsage{InputTokens: 10, OutputTokens: 4096}

	cancelled := call(lib.ProviderCallCancelled)
	cancelled.Usage = &lib.ProviderUsage{InputTokens: 10, OutputTokens: 5}

	testCases := []struct {
		name string

		generation *dao.Generation
		claimErr   error

		// The provider script. gets are returned in order, so a case can hold a call running for a
		// tick before it finishes.
		start     *lib.ProviderCall
		startErr  error
		gets      []*lib.ProviderCall
		getErr    error
		cancel    *lib.ProviderCall
		cancelErr error

		// Data-access failures, injected one at a time.
		recordErr, settleErr, requeueErr, usageErr error

		// An empty expectSettle asserts the generation is NOT settled.
		expectSettle  dao.GenerationStatus
		expectRequeue bool
		expectUsage   bool
		expectWorked  bool
		expectErr     error
	}{
		{
			name: "Success/FreshCall",

			generation: claimedGeneration(nil, false, 1),
			start:      succeededCall(),

			expectSettle: dao.GenerationStatusSucceeded,
			expectUsage:  true,
			expectWorked: true,
		},
		{
			// The whole point of recording the identifier: a generation that already has one is
			// resumed, so a crash costs a poll rather than a second priced call.
			name: "Success/ResumesInsteadOfStartingAgain",

			generation: claimedGeneration(&testResumeID, false, 1),
			gets:       []*lib.ProviderCall{succeededCall()},

			expectSettle: dao.GenerationStatusSucceeded,
			expectUsage:  true,
			expectWorked: true,
		},
		{
			name: "Success/EmptyQueue",
		},
		{
			// A single running poll has to be followed by another, or a generation settles on a
			// state the provider never reached.
			name: "Success/PollsUntilTerminal",

			generation: claimedGeneration(nil, false, 1),
			start:      call(lib.ProviderCallRunning),
			gets:       []*lib.ProviderCall{call(lib.ProviderCallRunning), succeededCall()},

			expectSettle: dao.GenerationStatusSucceeded,
			expectUsage:  true,
			expectWorked: true,
		},
		{
			// An output cap or a refusal is a failed generation but a real call: the tokens it
			// consumed still have to be recorded.
			name: "Success/IncompleteStillRecordsUsage",

			generation: claimedGeneration(nil, false, 1),
			start:      incomplete,

			expectSettle: dao.GenerationStatusFailed,
			expectUsage:  true,
			expectWorked: true,
		},
		{
			// A cancelled call is not a free call.
			name: "Success/CancelStopsTheCallAndRecordsWhatItSpent",

			generation: claimedGeneration(nil, true, 1),
			start:      call(lib.ProviderCallRunning),
			cancel:     cancelled,

			expectSettle: dao.GenerationStatusCancelled,
			expectUsage:  true,
			expectWorked: true,
		},
		{
			// Nothing reached the model, so there is nothing to account for.
			name: "Success/NoUsageWhenTheCallNeverRan",

			generation: claimedGeneration(nil, false, 1),
			start:      call(lib.ProviderCallFailed),

			expectSettle: dao.GenerationStatusFailed,
			expectWorked: true,
		},
		{
			// A state this worker does not treat as terminal must not strand the generation.
			name: "Success/SettlesAnUnrecognisedProviderState",

			generation: claimedGeneration(nil, true, 1),
			start:      call(lib.ProviderCallRunning),
			cancel:     call(lib.ProviderCallRunning),

			expectSettle: dao.GenerationStatusFailed,
			expectWorked: true,
		},
		{
			// A settle landing twice must not double-count; the row already being there is the
			// idempotent outcome, not a failure.
			name: "Success/ToleratesAReplayedUsageRow",

			generation: claimedGeneration(nil, false, 1),
			start:      succeededCall(),
			usageErr:   dao.ErrGenerationUsageExists,

			expectSettle: dao.GenerationStatusSucceeded,
			expectUsage:  true,
			expectWorked: true,
		},
		{
			// Retryable with an attempt left goes back to the queue, which clears the provider call
			// so the next run starts fresh.
			name: "Success/RetryableWithAttemptsLeftRequeues",

			generation: claimedGeneration(nil, false, 3),
			startErr:   lib.ErrProviderRetryable,

			expectRequeue: true,
			expectWorked:  true,
		},
		{
			// Nothing left to spend, so it settles rather than looping.
			name: "Success/RetryableWithNoAttemptLeftSettles",

			generation: claimedGeneration(nil, false, 1),
			startErr:   lib.ErrProviderRetryable,

			expectSettle: dao.GenerationStatusFailed,
			expectWorked: true,
		},
		{
			// Terminal: retrying only spends an attempt to be rejected again.
			name: "Success/TerminalFailureSettlesEvenWithAttemptsLeft",

			generation: claimedGeneration(nil, false, 3),
			startErr:   errFoo,

			expectSettle: dao.GenerationStatusFailed,
			expectWorked: true,
		},
		{
			// The orphan window. The provider accepted the call — it is running and will be billed
			// — and the write recording its identifier failed. Nothing can recover that operation,
			// so it settles rather than being retried into a second paid call.
			name: "Success/OrphansAPaidCallWhenTheRecordFails",

			generation: claimedGeneration(nil, false, 3),
			start:      call(lib.ProviderCallRunning),
			recordErr:  errFoo,

			expectSettle: dao.GenerationStatusFailed,
			expectWorked: true,
		},
		{
			// The provider may simply be unreachable, so with an attempt left it goes back.
			name: "Success/FailedReAttachRequeues",

			generation: claimedGeneration(&testResumeID, false, 3),
			getErr:     lib.ErrProviderRetryable,

			expectRequeue: true,
			expectWorked:  true,
		},
		{
			name: "Success/FailedCancelFallsBackToTheFailurePath",

			generation: claimedGeneration(nil, true, 1),
			start:      call(lib.ProviderCallRunning),
			cancelErr:  errFoo,

			expectSettle: dao.GenerationStatusFailed,
			expectWorked: true,
		},
		{
			// The usage row is never attempted once the transition failed; they are one unit.
			name: "Success/SettleFailureSkipsTheUsageRow",

			generation: claimedGeneration(nil, false, 1),
			start:      succeededCall(),
			settleErr:  errFoo,

			expectSettle: dao.GenerationStatusSucceeded,
			expectWorked: true,
		},
		{
			// The generation stays claimed; its lease lapses and the reaper recovers it, so the
			// failure costs a lease rather than the work.
			name: "Success/RequeueFailureLeavesItClaimed",

			generation: claimedGeneration(nil, false, 3),
			startErr:   lib.ErrProviderRetryable,
			requeueErr: errFoo,

			expectRequeue: true,
			expectWorked:  true,
		},
		{
			// The loop's own failure, not a generation's: it reports no work and tries again on the
			// next tick.
			name: "Error/ClaimFails",

			claimErr: errFoo,

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mocks := newWorkerMocks(t)

			var claimed []*dao.Generation
			if testCase.generation != nil {
				claimed = []*dao.Generation{testCase.generation}
			}

			mocks.claim.EXPECT().
				Exec(mock.Anything, &dao.GenerationClaimRequest{
					WorkerID: testWorkerID, Limit: 10, Lease: time.Minute,
				}).
				Return(claimed, testCase.claimErr)

			if testCase.start != nil || testCase.startErr != nil {
				mocks.provider.EXPECT().
					Start(mock.Anything, mock.Anything).
					Return(testCase.start, testCase.startErr)
			}

			for _, get := range testCase.gets {
				mocks.provider.EXPECT().Get(mock.Anything, mock.Anything).Return(get, nil).Once()
			}

			if testCase.getErr != nil {
				mocks.provider.EXPECT().Get(mock.Anything, mock.Anything).Return(nil, testCase.getErr)
			}

			if testCase.cancel != nil || testCase.cancelErr != nil {
				mocks.provider.EXPECT().
					Cancel(mock.Anything, testCallID).
					Return(testCase.cancel, testCase.cancelErr)
			}

			// Recorded whenever a fresh call was started and accepted.
			if testCase.start != nil {
				mocks.record.EXPECT().
					Exec(mock.Anything, mock.Anything).
					Return(testCase.generation, testCase.recordErr)
			}

			if testCase.expectSettle != "" {
				mocks.settle.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSettleRequest) bool {
						return request.Status == testCase.expectSettle && request.WorkerID == testWorkerID
					})).
					Return(testCase.generation, testCase.settleErr)
			}

			if testCase.expectUsage {
				mocks.provider.EXPECT().Name().Return("openai")
				mocks.usage.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationUsageInsertRequest) bool {
						// The model that actually billed, not the alias the request asked for.
						return request.Model == "a-model-snapshot" && request.Provider == "openai"
					})).
					Return(&dao.GenerationUsage{}, testCase.usageErr)
			}

			if testCase.expectRequeue {
				mocks.requeue.EXPECT().
					Exec(mock.Anything, mock.Anything).
					Return(testCase.generation, testCase.requeueErr)
			}

			worked, err := mocks.worker(t).RunOnce(t.Context())
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expectWorked, worked)

			if testCase.expectSettle == "" {
				mocks.settle.AssertNotCalled(t, "Exec", mock.Anything, mock.Anything)
			}

			if !testCase.expectRequeue {
				mocks.requeue.AssertNotCalled(t, "Exec", mock.Anything, mock.Anything)
			}

			if !testCase.expectUsage {
				mocks.usage.AssertNotCalled(t, "Exec", mock.Anything, mock.Anything)
			}

			mocks.assertExpectations(t)
		})
	}
}

// Shutting down mid-run must neither settle nor requeue. Settling throws away an operation already
// paid for; a requeue clears the identifier that lets the next run resume it. Leaving the claim
// alone is what makes a rolling deploy free — the lease lapses, the reaper recovers the generation
// with its provider call preserved, and the next claim re-attaches.
//
// Out of the table because it cancels the context from inside a mock, mid-run.
func TestWorkerStopsCleanlyOnShutdown(t *testing.T) {
	t.Parallel()

	generation := claimedGeneration(nil, false, 3)
	mocks := newWorkerMocks(t)

	ctx, cancel := context.WithCancel(t.Context())

	mocks.claim.EXPECT().Exec(mock.Anything, mock.Anything).Return([]*dao.Generation{generation}, nil)
	mocks.provider.EXPECT().
		Start(mock.Anything, mock.Anything).
		Return(call(lib.ProviderCallRunning), nil)
	mocks.record.EXPECT().
		Exec(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, *dao.GenerationRecordProviderCallRequest) (*dao.Generation, error) {
			// The identifier is recorded, and only then does the process go away. This is the
			// window the whole re-attach design exists to survive.
			cancel()

			return generation, nil
		})

	_, err := mocks.worker(t).RunOnce(ctx)
	require.NoError(t, err)

	mocks.settle.AssertNotCalled(t, "Exec", mock.Anything, mock.Anything)
	mocks.requeue.AssertNotCalled(t, "Exec", mock.Anything, mock.Anything)
	mocks.assertExpectations(t)
}

// A batch is already claimed, so one generation failing must not strand the others until their
// leases lapse. Out of the table because it is the only case with more than one generation.
func TestWorkerContinuesTheBatchAfterOneFailure(t *testing.T) {
	t.Parallel()

	first := claimedGeneration(nil, false, 1)
	second := claimedGeneration(nil, false, 1)
	second.ID = uuid.MustParse("01999999-0000-7000-8000-000000000002")

	mocks := newWorkerMocks(t)

	mocks.claim.EXPECT().Exec(mock.Anything, mock.Anything).Return([]*dao.Generation{first, second}, nil)
	mocks.provider.EXPECT().Start(mock.Anything, mock.Anything).Return(nil, errFoo).Once()
	mocks.provider.EXPECT().Start(mock.Anything, mock.Anything).Return(succeededCall(), nil).Once()
	mocks.provider.EXPECT().Name().Return("openai")
	mocks.record.EXPECT().Exec(mock.Anything, mock.Anything).Return(second, nil).Once()
	mocks.settle.EXPECT().Exec(mock.Anything, mock.Anything).Return(first, nil).Twice()
	mocks.usage.EXPECT().Exec(mock.Anything, mock.Anything).Return(&dao.GenerationUsage{}, nil).Once()

	worked, err := mocks.worker(t).RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, worked)

	mocks.assertExpectations(t)
}

func TestNewWorker(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		config core.WorkerConfig

		expectErr error
	}{
		{
			name:   "Success",
			config: testWorkerConfig(),
		},
		{
			name: "Error/NoID",
			config: core.WorkerConfig{
				Lease: time.Minute, BatchSize: 10, PollInterval: time.Second, Retention: time.Hour,
			},
			expectErr: core.ErrInvalidRequest,
		},
		{
			// The ceiling the data access no longer enforces. A lease longer than any generation we
			// run leaves a stranded claim invisible for an hour.
			name: "Error/LeaseOverCeiling",
			config: core.WorkerConfig{
				ID: testWorkerID, Lease: core.ClaimLeaseCeiling + time.Second,
				BatchSize: 10, PollInterval: time.Second, Retention: time.Hour,
			},
			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/BatchOverCeiling",
			config: core.WorkerConfig{
				ID: testWorkerID, Lease: time.Minute,
				BatchSize: core.ClaimLimitCeiling + 1, PollInterval: time.Second, Retention: time.Hour,
			},
			expectErr: core.ErrInvalidRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mocks := newWorkerMocks(t)

			worker, err := core.NewWorker(
				testCase.config, mocks.provider, transactiontest.NewTransactor(), mocks.daos(),
			)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, worker)

				return
			}

			require.NotNil(t, worker)
		})
	}
}
