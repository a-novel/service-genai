package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestGenerationSweep(t *testing.T) {
	t.Parallel()

	config := core.GenerationSweepConfig{Interval: time.Minute, BatchSize: 10}

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
			// One generation's failure must not strand the rest of the batch.
			name: "Error/CheckContinuesBatch",

			swept: []*dao.Generation{first, second},
			checkMocks: []checkMock{
				{generation: first, err: errFoo},
				{generation: second},
			},

			expectErr: errFoo,
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
					Interval: config.Interval,
					Limit:    config.BatchSize,
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

			result, err := core.NewGenerationSweep(config, sweepDao, check).
				Exec(t.Context(), &core.GenerationSweepRequest{})
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expect, result)

			sweepDao.AssertExpectations(t)
			check.AssertExpectations(t)
		})
	}
}
