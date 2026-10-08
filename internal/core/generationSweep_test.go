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

	config := core.GenerationSweepConfig{Interval: time.Minute, BatchSize: 10, ProviderEpoch: testEpoch}

	first := runningGeneration(1)
	second := pendingGeneration()

	type checkMock struct {
		generation *dao.Generation
		err        error
		panics     bool
	}

	testCases := []struct {
		name string

		config core.GenerationSweepConfig

		swept    []*dao.Generation
		sweepErr error

		checkMocks []checkMock

		expectWorked   bool
		expectErr      error
		expectBuildErr error
	}{
		{
			name: "Success",

			config: config,
			swept:  []*dao.Generation{first, second},
			checkMocks: []checkMock{
				{generation: first},
				{generation: second},
			},

			expectWorked: true,
		},
		{
			name: "Success/Empty",

			config: config,
			swept:  []*dao.Generation{},
		},
		{
			// One generation's failure must not strand the rest of the batch.
			name: "Error/CheckContinuesBatch",

			config: config,
			swept:  []*dao.Generation{first, second},
			checkMocks: []checkMock{
				{generation: first, err: errFoo},
				{generation: second},
			},

			expectWorked: true,
			expectErr:    errFoo,
		},
		{
			// A panic stays with its generation: the process keeps running and the batch progresses.
			name: "Success/CheckPanics",

			config: config,
			swept:  []*dao.Generation{first, second},
			checkMocks: []checkMock{
				{generation: first, panics: true},
				{generation: second},
			},

			expectWorked: true,
		},
		{
			name: "Error/Sweep",

			config:   config,
			sweepErr: errFoo,

			expectErr: errFoo,
		},
		{
			// A finished result stays retrievable for about ten minutes; a slower sweep would lose it.
			name: "Error/IntervalAboveCeiling",

			config: core.GenerationSweepConfig{Interval: 10 * time.Minute, BatchSize: 10, ProviderEpoch: testEpoch},

			expectBuildErr: core.ErrInvalidRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			sweepDao := coremocks.NewMockGenerationSweepDao(t)
			check := coremocks.NewMockGenerationSweepServiceCheck(t)

			sweep, err := core.NewGenerationSweep(testCase.config, sweepDao, check)
			require.ErrorIs(t, err, testCase.expectBuildErr)

			if testCase.expectBuildErr != nil {
				return
			}

			sweepDao.EXPECT().
				Exec(mock.Anything, &dao.GenerationSweepRequest{
					Interval:      testCase.config.Interval,
					Limit:         testCase.config.BatchSize,
					ProviderEpoch: testEpoch,
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

			worked, err := sweep.RunOnce(t.Context())
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expectWorked, worked)

			sweepDao.AssertExpectations(t)
			check.AssertExpectations(t)
		})
	}
}
