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

	sweep := config.Sweep{Interval: time.Minute, BatchSize: 10}

	first := runningGeneration(1)
	second := pendingGeneration()

	type checkMock struct {
		generation *dao.Generation
		err        error
		panics     bool
	}

	testCases := []struct {
		name string

		swept    []*dao.Generation
		sweepErr error
		// stopping runs the pass in a process already stopping.
		stopping bool

		checkMocks []checkMock

		expect    *core.GenerationSweepResult
		expectErr error
	}{
		{
			name: "Success",

			swept: []*dao.Generation{first, second},
			checkMocks: []checkMock{
				{generation: first},
				{generation: second},
			},

			expect: &core.GenerationSweepResult{Checked: 2},
		},
		{
			name: "Success/Empty",

			swept: []*dao.Generation{},

			expect: &core.GenerationSweepResult{},
		},
		{
			// One generation's failure stays with it: the rest of the batch progresses, and the pass
			// reports work found, so a backlog keeps draining.
			name: "Success/CheckFails",

			swept: []*dao.Generation{first, second},
			checkMocks: []checkMock{
				{generation: first, err: errFoo},
				{generation: second},
			},

			expect: &core.GenerationSweepResult{Checked: 2},
		},
		{
			// A panic stays with its generation: the process keeps running and the batch progresses.
			name: "Success/CheckPanics",

			swept: []*dao.Generation{first, second},
			checkMocks: []checkMock{
				{generation: first, panics: true},
				{generation: second},
			},

			expect: &core.GenerationSweepResult{Checked: 2},
		},
		{
			// A stopping process launches no check, so no start leaves on its way out.
			name: "Success/Stopping",

			swept:    []*dao.Generation{first, second},
			stopping: true,

			expect: &core.GenerationSweepResult{Checked: 2},
		},
		{
			name: "Error/Sweep",

			sweepErr: errFoo,

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			sweepDao := coremocks.NewMockGenerationSweepDao(t)
			check := coremocks.NewMockGenerationSweepServiceCheck(t)

			sweepDao.EXPECT().
				Exec(mock.Anything, &dao.GenerationSweepRequest{
					Interval: sweep.Interval,
					Limit:    sweep.BatchSize,
				}).
				Return(testCase.swept, testCase.sweepErr)

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

			ctx, stop := context.WithCancel(t.Context())
			defer stop()

			if testCase.stopping {
				stop()
			}

			result, err := core.NewGenerationSweep(sweepDao, check, sweep).Exec(ctx, &core.GenerationSweepRequest{})
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expect, result)

			sweepDao.AssertExpectations(t)
			check.AssertExpectations(t)
		})
	}
}
