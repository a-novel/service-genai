package core_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestGenerationSweep(t *testing.T) {
	t.Parallel()

	sweep := config.Sweep{Interval: time.Minute, BatchSize: 2, Concurrency: 8}

	first := runningGeneration()
	first.ID = uuid.MustParse("01999999-0000-7000-8000-000000000011")
	second := startingGeneration(time.Second)
	second.ID = uuid.MustParse("01999999-0000-7000-8000-000000000012")
	third := runningGeneration()
	third.ID = uuid.MustParse("01999999-0000-7000-8000-000000000013")

	type batchMock struct {
		// after is the generation the batch follows, the last of the batch before.
		after uuid.UUID
		swept []*dao.Generation
		err   error
		// shutdown ends the call's context while the batch is taken, as a stopping server does.
		shutdown bool
	}

	type checkMock struct {
		generation *dao.Generation
		err        error
		panics     bool
	}

	testCases := []struct {
		name string

		// lockErr refuses the leader role, as when another sweep holds it.
		lockErr error
		batches []batchMock
		// deadline bounds the call; zero leaves it unbounded.
		deadline time.Duration

		checkMocks []checkMock

		expect    *core.GenerationSweepResult
		expectErr error
	}{
		{
			name: "Success",

			batches: []batchMock{{swept: []*dao.Generation{first}}},
			checkMocks: []checkMock{
				{generation: first},
			},

			expect: &core.GenerationSweepResult{Checked: 1},
		},
		{
			name: "Success/Empty",

			batches: []batchMock{{swept: []*dao.Generation{}}},

			expect: &core.GenerationSweepResult{},
		},
		{
			// A full batch may leave more behind it, so another follows until one comes back short.
			name: "Success/Backlog",

			batches: []batchMock{
				{swept: []*dao.Generation{first, second}},
				{after: second.ID, swept: []*dao.Generation{third}},
			},
			checkMocks: []checkMock{
				{generation: first},
				{generation: second},
				{generation: third},
			},

			expect: &core.GenerationSweepResult{Checked: 3},
		},
		{
			// One generation's failure stays with it: the rest of the batch progresses.
			name: "Success/CheckFails",

			batches: []batchMock{{swept: []*dao.Generation{first}}},
			checkMocks: []checkMock{
				{generation: first, err: errFoo},
			},

			expect: &core.GenerationSweepResult{Checked: 1},
		},
		{
			// A panic stays with its generation: the process keeps running and the batch progresses.
			name: "Success/CheckPanics",

			batches: []batchMock{{swept: []*dao.Generation{first}}},
			checkMocks: []checkMock{
				{generation: first, panics: true},
			},

			expect: &core.GenerationSweepResult{Checked: 1},
		},
		{
			// A stopping server starts no check, not even of a batch already taken: it waits for a
			// later call.
			name: "Success/ShutdownStopsDispatch",

			batches: []batchMock{{swept: []*dao.Generation{first, second}, shutdown: true}},

			expect: &core.GenerationSweepResult{Checked: 2},
		},
		{
			// A query cut by the shutdown is no failure: the call ends with what it checked.
			name: "Success/ShutdownDuringQuery",

			batches: []batchMock{{err: context.Canceled, shutdown: true}},

			expect: &core.GenerationSweepResult{},
		},
		{
			// Another replica leads the run, and checks every generation this one would.
			name: "Success/LedElsewhere",

			lockErr: dao.ErrGenerationSweepLocked,

			expect: &core.GenerationSweepResult{},
		},
		{
			name: "Error/Lock",

			lockErr: errFoo,

			expectErr: errFoo,
		},
		{
			// A caller that cannot wait out a check gets none, and learns its deadline is the cause.
			name: "Error/DeadlineTooShort",

			deadline: time.Second,

			expectErr: core.ErrSweepDeadlineTooShort,
		},
		{
			name: "Error/Sweep",

			batches: []batchMock{{err: errFoo}},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			sweepDao := coremocks.NewMockGenerationSweepDao(t)
			lockDao := coremocks.NewMockGenerationSweepDaoLock(t)
			unlockDao := coremocks.NewMockGenerationSweepDaoUnlock(t)
			check := coremocks.NewMockGenerationSweepServiceCheck(t)

			// Every run that may start a check takes the leader role first, and releases it at the end,
			// however the run ended.
			if testCase.deadline == 0 {
				lock := &dao.GenerationSweepLock{}

				lockDao.EXPECT().Exec(mock.Anything, &dao.GenerationSweepLockRequest{}).
					Return(lo.Ternary(testCase.lockErr == nil, lock, nil), testCase.lockErr).Once()

				if testCase.lockErr == nil {
					unlockDao.EXPECT().
						Exec(mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil }),
							&dao.GenerationSweepUnlockRequest{Lock: lock}).
						Return(nil).Once()
				}
			}

			for _, batch := range testCase.batches {
				sweepDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationSweepRequest{After: batch.after, Limit: sweep.BatchSize}).
					RunAndReturn(func(context.Context, *dao.GenerationSweepRequest) ([]*dao.Generation, error) {
						if batch.shutdown {
							cancel()
						}

						return batch.swept, batch.err
					}).
					Once()
			}

			for _, checkMock := range testCase.checkMocks {
				call := check.EXPECT().Exec(mock.Anything, &core.GenerationCheckRequest{Generation: checkMock.generation})

				if checkMock.panics {
					call.RunAndReturn(func(context.Context, *core.GenerationCheckRequest) (*dao.Generation, error) {
						panic("check exploded")
					}).Once()
				} else {
					call.Return(checkMock.generation, checkMock.err).Once()
				}
			}

			if testCase.deadline > 0 {
				var cancelDeadline context.CancelFunc

				ctx, cancelDeadline = context.WithTimeout(ctx, testCase.deadline)
				defer cancelDeadline()
			}

			service := core.NewGenerationSweep(sweepDao, lockDao, unlockDao, check, sweep)

			result, err := service.Exec(ctx, &core.GenerationSweepRequest{})
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expect, result)

			mock.AssertExpectationsForObjects(t, sweepDao, lockDao, unlockDao, check)
		})
	}

	// A scheduler call that runs long overlaps the next one. Both share the process's concurrency,
	// which the connection pool sizes, rather than each running as many checks.
	t.Run("Success/OverlappingCallsShareConcurrency", func(t *testing.T) {
		t.Parallel()

		const calls = 3

		shared := config.Sweep{Interval: time.Minute, BatchSize: 2, Concurrency: 1}

		sweepDao := coremocks.NewMockGenerationSweepDao(t)
		sweepDao.EXPECT().Exec(mock.Anything, mock.Anything).Return([]*dao.Generation{first}, nil).Times(calls)

		// The leader role is the database's: one process's overlapping calls each take it in turn here.
		lockDao := coremocks.NewMockGenerationSweepDaoLock(t)
		lockDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(&dao.GenerationSweepLock{}, nil).Times(calls)

		unlockDao := coremocks.NewMockGenerationSweepDaoUnlock(t)
		unlockDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(nil).Times(calls)

		var (
			running    atomic.Int32
			overlapped atomic.Bool
		)

		check := coremocks.NewMockGenerationSweepServiceCheck(t)
		check.EXPECT().Exec(mock.Anything, mock.Anything).
			RunAndReturn(func(context.Context, *core.GenerationCheckRequest) (*dao.Generation, error) {
				if running.Add(1) > 1 {
					overlapped.Store(true)
				}

				time.Sleep(20 * time.Millisecond)
				running.Add(-1)

				return first, nil
			}).
			Times(calls)

		service := core.NewGenerationSweep(sweepDao, lockDao, unlockDao, check, shared)

		var overlapping sync.WaitGroup

		for range calls {
			overlapping.Go(func() {
				_, err := service.Exec(t.Context(), &core.GenerationSweepRequest{})
				require.NoError(t, err)
			})
		}

		overlapping.Wait()
		require.False(t, overlapped.Load())
	})
}
