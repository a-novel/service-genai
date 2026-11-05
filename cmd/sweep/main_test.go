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
			daoLock := coremocks.NewMockGenerationSweepDaoLock(t)
			daoUnlock := coremocks.NewMockGenerationSweepDaoUnlock(t)
			serviceCheck := coremocks.NewMockGenerationSweepServiceCheck(t)

			if testCase.daoMock != nil {
				lock := &dao.GenerationSweepLock{}

				daoLock.EXPECT().Exec(mock.Anything, &dao.GenerationSweepLockRequest{}).Return(lock, nil)
				daoSweep.EXPECT().
					Exec(mock.Anything, &dao.GenerationSweepRequest{Limit: sweep.BatchSize}).
					Return(nil, testCase.daoMock.err)
				daoUnlock.EXPECT().Exec(mock.Anything, &dao.GenerationSweepUnlockRequest{Lock: lock}).Return(nil)
			}

			service := core.NewGenerationSweep(daoSweep, daoLock, daoUnlock, serviceCheck, sweep)

			worked, err := sweepRun(service, testCase.downtimeStart)(t.Context())
			require.ErrorIs(t, err, testCase.expectErr)
			require.False(t, worked)

			mock.AssertExpectationsForObjects(t, daoSweep, daoLock, daoUnlock, serviceCheck)
		})
	}
}
