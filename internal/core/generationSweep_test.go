package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestGenerationSweep(t *testing.T) {
	t.Parallel()

	sweep := config.Sweep{Interval: time.Minute, BatchSize: 2}

	first := runningGeneration(1)
	second := pendingGeneration()
	third := runningGeneration(2)

	type batchMock struct {
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
				{swept: []*dao.Generation{third}},
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
			check := coremocks.NewMockGenerationSweepServiceCheck(t)

			for _, batch := range testCase.batches {
				sweepDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationSweepRequest{
						Interval: sweep.Interval,
						Limit:    sweep.BatchSize,
					}).
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

			result, err := core.NewGenerationSweep(sweepDao, check, sweep).Exec(ctx, &core.GenerationSweepRequest{})
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expect, result)

			sweepDao.AssertExpectations(t)
			check.AssertExpectations(t)
		})
	}
}
