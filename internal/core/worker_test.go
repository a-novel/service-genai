package core_test

import (
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

const testWorkerID = "worker-1"

func testWorkerConfig() core.WorkerConfig {
	return core.WorkerConfig{
		ID:           testWorkerID,
		Lease:        time.Minute,
		BatchSize:    10,
		PollInterval: time.Millisecond,
		Retention:    7 * 24 * time.Hour,
	}
}

func claimedGeneration(providerCallID *string, cancelRequested bool) *dao.Generation {
	generation := &dao.Generation{
		ID:             uuid.MustParse("01999999-0000-7000-8000-000000000001"),
		OwnerID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Purpose:        "studio.generation",
		Request:        json.RawMessage(`{"model": "a-model"}`),
		Status:         dao.GenerationStatusRunning,
		Attempt:        1,
		MaxAttempts:    1,
		ProviderCallID: providerCallID,
	}

	if cancelRequested {
		now := time.Now()
		generation.CancelRequestedAt = &now
	}

	return generation
}

func succeededCall() *lib.ProviderCall {
	return &lib.ProviderCall{
		ID:     "resp_1",
		State:  lib.ProviderCallSucceeded,
		Model:  "a-model-snapshot",
		Output: json.RawMessage(`{"text": "done"}`),
		Usage: &lib.ProviderUsage{
			InputTokens: 1000, CachedInputTokens: 200,
			OutputTokens: 500, ReasoningTokens: 100,
		},
	}
}

func TestWorker(t *testing.T) {
	t.Parallel()

	resumeID := "resp_resumed"

	type providerMock struct {
		start  *lib.ProviderCall
		get    *lib.ProviderCall
		cancel *lib.ProviderCall
		err    error
	}

	testCases := []struct {
		name string

		claimed []*dao.Generation

		provider *providerMock

		// expectStart and expectGet pin which path the worker took. Getting these the wrong way
		// round is what a crash re-attach exists to prevent.
		expectStart  bool
		expectGet    bool
		expectCancel bool
		// expectRecord is true when a fresh provider call had its id recorded.
		expectRecord bool

		expectSettle  dao.GenerationStatus
		expectUsage   bool
		expectRequeue bool
	}{
		{
			name: "Success/FreshCall",

			claimed:  []*dao.Generation{claimedGeneration(nil, false)},
			provider: &providerMock{start: succeededCall()},

			expectStart:  true,
			expectRecord: true,
			expectSettle: dao.GenerationStatusSucceeded,
			expectUsage:  true,
		},
		{
			// The whole point of recording the identifier: a generation that already has one is
			// resumed, so a crash costs a poll rather than a second priced call.
			name: "Success/ResumesInsteadOfStartingAgain",

			claimed:  []*dao.Generation{claimedGeneration(&resumeID, false)},
			provider: &providerMock{get: succeededCall()},

			expectGet:    true,
			expectSettle: dao.GenerationStatusSucceeded,
			expectUsage:  true,
		},
		{
			name: "Success/EmptyQueue",

			claimed: nil,
		},
		{
			// An output cap or a refusal is a failed generation but a real call: the tokens it
			// consumed still have to be recorded.
			name: "Success/IncompleteStillRecordsUsage",

			claimed: []*dao.Generation{claimedGeneration(nil, false)},
			provider: &providerMock{start: &lib.ProviderCall{
				ID: "resp_1", State: lib.ProviderCallIncomplete, Model: "a-model-snapshot",
				Reason: "max_output_tokens",
				Usage:  &lib.ProviderUsage{InputTokens: 10, OutputTokens: 4096},
			}},

			expectStart:  true,
			expectRecord: true,
			expectSettle: dao.GenerationStatusFailed,
			expectUsage:  true,
		},
		{
			// A cancelled call is not a free call.
			name: "Success/CancelStopsTheCallAndRecordsWhatItSpent",

			claimed: []*dao.Generation{claimedGeneration(nil, true)},
			provider: &providerMock{
				start: &lib.ProviderCall{ID: "resp_1", State: lib.ProviderCallRunning},
				cancel: &lib.ProviderCall{
					ID: "resp_1", State: lib.ProviderCallCancelled, Model: "a-model-snapshot",
					Usage: &lib.ProviderUsage{InputTokens: 10, OutputTokens: 5},
				},
			},

			expectStart:  true,
			expectRecord: true,
			expectCancel: true,
			expectSettle: dao.GenerationStatusCancelled,
			expectUsage:  true,
		},
		{
			// Nothing reached the model, so there is nothing to account for.
			name: "Success/NoUsageWhenTheCallNeverRan",

			claimed: []*dao.Generation{claimedGeneration(nil, false)},
			provider: &providerMock{start: &lib.ProviderCall{
				ID: "resp_1", State: lib.ProviderCallFailed, Reason: "the model failed",
			}},

			expectStart:  true,
			expectRecord: true,
			expectSettle: dao.GenerationStatusFailed,
		},
		{
			// Retryable with an attempt left goes back to the queue, which clears the provider call
			// so the next run starts fresh.
			name: "Success/RetryableWithAttemptsLeftRequeues",

			claimed: []*dao.Generation{func() *dao.Generation {
				generation := claimedGeneration(nil, false)
				generation.MaxAttempts = 3

				return generation
			}()},
			provider: &providerMock{err: lib.ErrProviderRetryable},

			expectStart:   true,
			expectRequeue: true,
		},
		{
			// Nothing left to spend, so it settles rather than looping.
			name: "Success/RetryableWithNoAttemptLeftSettles",

			claimed:  []*dao.Generation{claimedGeneration(nil, false)},
			provider: &providerMock{err: lib.ErrProviderRetryable},

			expectStart:  true,
			expectSettle: dao.GenerationStatusFailed,
		},
		{
			// Terminal: retrying only spends an attempt to be rejected again.
			name: "Success/TerminalFailureSettlesEvenWithAttemptsLeft",

			claimed: []*dao.Generation{func() *dao.Generation {
				generation := claimedGeneration(nil, false)
				generation.MaxAttempts = 3

				return generation
			}()},
			provider: &providerMock{err: errFoo},

			expectStart:  true,
			expectSettle: dao.GenerationStatusFailed,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := libmocks.NewMockProvider(t)
			claimDao := coremocks.NewMockWorkerClaimDao(t)
			recordDao := coremocks.NewMockWorkerRecordProviderCallDao(t)
			settleDao := coremocks.NewMockWorkerSettleDao(t)
			requeueDao := coremocks.NewMockWorkerRequeueDao(t)
			usageDao := coremocks.NewMockWorkerUsageInsertDao(t)

			claimDao.EXPECT().
				Exec(mock.Anything, &dao.GenerationClaimRequest{
					WorkerID: testWorkerID, Limit: 10, Lease: time.Minute,
				}).
				Return(testCase.claimed, nil)

			if testCase.expectStart {
				provider.EXPECT().
					Start(mock.Anything, mock.Anything).
					Return(testCase.provider.start, testCase.provider.err)
			}

			if testCase.expectGet {
				provider.EXPECT().Get(mock.Anything, resumeID).Return(testCase.provider.get, nil)
			}

			if testCase.expectCancel {
				provider.EXPECT().Cancel(mock.Anything, "resp_1").Return(testCase.provider.cancel, nil)
			}

			if testCase.expectRecord {
				recordDao.EXPECT().
					Exec(mock.Anything, mock.Anything).
					Return(testCase.claimed[0], nil)
			}

			if testCase.expectSettle != "" {
				settleDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSettleRequest) bool {
						return request.Status == testCase.expectSettle && request.WorkerID == testWorkerID
					})).
					Return(testCase.claimed[0], nil)
			}

			if testCase.expectUsage {
				provider.EXPECT().Name().Return("openai")
				usageDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationUsageInsertRequest) bool {
						// The model that actually billed, not the alias the request asked for.
						return request.Model == "a-model-snapshot" && request.Provider == "openai"
					})).
					Return(&dao.GenerationUsage{}, nil)
			}

			if testCase.expectRequeue {
				requeueDao.EXPECT().
					Exec(mock.Anything, mock.Anything).
					Return(testCase.claimed[0], nil)
			}

			worker, err := core.NewWorker(
				testWorkerConfig(), provider, transactiontest.NewTransactor(),
				claimDao, recordDao, settleDao, requeueDao, usageDao,
			)
			require.NoError(t, err)

			worked, err := worker.RunOnce(t.Context())
			require.NoError(t, err)
			require.Equal(t, len(testCase.claimed) > 0, worked)

			provider.AssertExpectations(t)
			claimDao.AssertExpectations(t)
			recordDao.AssertExpectations(t)
			settleDao.AssertExpectations(t)
			requeueDao.AssertExpectations(t)
			usageDao.AssertExpectations(t)
		})
	}
}

// The settle and the usage row are one unit of work. A transition that lands without its usage row
// is a charge nothing accounts for, so a failing usage write must take the settle with it.
func TestWorkerSettleIsAtomic(t *testing.T) {
	t.Parallel()

	generation := claimedGeneration(nil, false)

	provider := libmocks.NewMockProvider(t)
	claimDao := coremocks.NewMockWorkerClaimDao(t)
	recordDao := coremocks.NewMockWorkerRecordProviderCallDao(t)
	settleDao := coremocks.NewMockWorkerSettleDao(t)
	requeueDao := coremocks.NewMockWorkerRequeueDao(t)
	usageDao := coremocks.NewMockWorkerUsageInsertDao(t)

	claimDao.EXPECT().Exec(mock.Anything, mock.Anything).Return([]*dao.Generation{generation}, nil)
	provider.EXPECT().Start(mock.Anything, mock.Anything).Return(succeededCall(), nil)
	provider.EXPECT().Name().Return("openai")
	recordDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(generation, nil)
	settleDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(generation, nil)
	usageDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(nil, errFoo)

	worker, err := core.NewWorker(
		testWorkerConfig(), provider, transactiontest.NewTransactor(),
		claimDao, recordDao, settleDao, requeueDao, usageDao,
	)
	require.NoError(t, err)

	// The run reports work found; the failure is recorded on the generation, and the transaction
	// the settle ran in is the thing that rolls back.
	_, err = worker.RunOnce(t.Context())
	require.NoError(t, err)

	usageDao.AssertExpectations(t)
	settleDao.AssertExpectations(t)
}

// A settle landing twice must not double-count. The usage insert reports the row already exists,
// and that is the idempotent outcome rather than a failure.
func TestWorkerSettleToleratesAReplayedUsageRow(t *testing.T) {
	t.Parallel()

	generation := claimedGeneration(nil, false)

	provider := libmocks.NewMockProvider(t)
	claimDao := coremocks.NewMockWorkerClaimDao(t)
	recordDao := coremocks.NewMockWorkerRecordProviderCallDao(t)
	settleDao := coremocks.NewMockWorkerSettleDao(t)
	requeueDao := coremocks.NewMockWorkerRequeueDao(t)
	usageDao := coremocks.NewMockWorkerUsageInsertDao(t)

	claimDao.EXPECT().Exec(mock.Anything, mock.Anything).Return([]*dao.Generation{generation}, nil)
	provider.EXPECT().Start(mock.Anything, mock.Anything).Return(succeededCall(), nil)
	provider.EXPECT().Name().Return("openai")
	recordDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(generation, nil)
	settleDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(generation, nil)
	usageDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(nil, dao.ErrGenerationUsageExists)

	worker, err := core.NewWorker(
		testWorkerConfig(), provider, transactiontest.NewTransactor(),
		claimDao, recordDao, settleDao, requeueDao, usageDao,
	)
	require.NoError(t, err)

	worked, err := worker.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, worked)
}

// A batch is already claimed, so one generation failing must not strand the others until their
// leases lapse.
func TestWorkerContinuesTheBatchAfterOneFailure(t *testing.T) {
	t.Parallel()

	first := claimedGeneration(nil, false)
	second := claimedGeneration(nil, false)
	second.ID = uuid.MustParse("01999999-0000-7000-8000-000000000002")

	provider := libmocks.NewMockProvider(t)
	claimDao := coremocks.NewMockWorkerClaimDao(t)
	recordDao := coremocks.NewMockWorkerRecordProviderCallDao(t)
	settleDao := coremocks.NewMockWorkerSettleDao(t)
	requeueDao := coremocks.NewMockWorkerRequeueDao(t)
	usageDao := coremocks.NewMockWorkerUsageInsertDao(t)

	claimDao.EXPECT().Exec(mock.Anything, mock.Anything).Return([]*dao.Generation{first, second}, nil)
	provider.EXPECT().Start(mock.Anything, mock.Anything).Return(nil, errFoo).Once()
	provider.EXPECT().Start(mock.Anything, mock.Anything).Return(succeededCall(), nil).Once()
	provider.EXPECT().Name().Return("openai")
	recordDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(second, nil).Once()
	settleDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(first, nil).Twice()
	usageDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(&dao.GenerationUsage{}, nil).Once()

	worker, err := core.NewWorker(
		testWorkerConfig(), provider, transactiontest.NewTransactor(),
		claimDao, recordDao, settleDao, requeueDao, usageDao,
	)
	require.NoError(t, err)

	worked, err := worker.RunOnce(t.Context())
	require.NoError(t, err)
	require.True(t, worked)

	provider.AssertExpectations(t)
	settleDao.AssertExpectations(t)
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
			config: func() core.WorkerConfig {
				config := testWorkerConfig()
				config.ID = ""

				return config
			}(),
			expectErr: core.ErrInvalidRequest,
		},
		{
			// The ceiling the data access no longer enforces. A lease longer than any generation we
			// run leaves a stranded claim invisible for an hour.
			name: "Error/LeaseOverCeiling",
			config: func() core.WorkerConfig {
				config := testWorkerConfig()
				config.Lease = core.ClaimLeaseCeiling + time.Second

				return config
			}(),
			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/BatchOverCeiling",
			config: func() core.WorkerConfig {
				config := testWorkerConfig()
				config.BatchSize = core.ClaimLimitCeiling + 1

				return config
			}(),
			expectErr: core.ErrInvalidRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			worker, err := core.NewWorker(
				testCase.config,
				libmocks.NewMockProvider(t),
				transactiontest.NewTransactor(),
				coremocks.NewMockWorkerClaimDao(t),
				coremocks.NewMockWorkerRecordProviderCallDao(t),
				coremocks.NewMockWorkerSettleDao(t),
				coremocks.NewMockWorkerRequeueDao(t),
				coremocks.NewMockWorkerUsageInsertDao(t),
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
