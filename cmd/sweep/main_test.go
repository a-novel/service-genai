package main

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestSweepRun(t *testing.T) {
	t.Parallel()

	errFoo := errors.New("foo")

	started := time.Now().Add(-time.Minute)
	sweep := config.Sweep{Interval: time.Minute, BatchSize: 50, Concurrency: 1}

	type daoMock struct {
		err error
	}

	testCases := []struct {
		name string

		downtimeStart *time.Time

		daoMock *daoMock

		expectErr error
	}{
		{
			name: "Success",

			daoMock: &daoMock{},
		},
		{
			name: "Success/Downtime",

			downtimeStart: &started,
		},
		{
			name: "Error/Sweep",

			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			daoSweep := coremocks.NewMockGenerationSweepDao(t)
			serviceCheck := coremocks.NewMockGenerationSweepServiceCheck(t)

			if testCase.daoMock != nil {
				daoSweep.EXPECT().
					Exec(mock.Anything, &dao.GenerationSweepRequest{Interval: sweep.Interval / 2, Limit: sweep.BatchSize}).
					Return(nil, testCase.daoMock.err)
			}

			service := core.NewGenerationSweep(daoSweep, serviceCheck, sweep)

			worked, err := sweepRun(service, testCase.downtimeStart)(t.Context())
			require.ErrorIs(t, err, testCase.expectErr)
			require.False(t, worked)

			daoSweep.AssertExpectations(t)
			serviceCheck.AssertExpectations(t)
		})
	}
}
