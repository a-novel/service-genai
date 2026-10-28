package main

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/sentryf/sentryftest"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

//nolint:paralleltest // Records through the process-wide Sentry reporter.
func TestSweepRun(t *testing.T) {
	errFoo := errors.New("foo")

	started := time.Now().Add(-time.Minute)
	sweep := config.Sweep{Interval: time.Minute, BatchSize: 50, Concurrency: 1}

	type daoMock struct {
		err    error
		panics bool
	}

	testCases := []struct {
		name string

		downtimeStart *time.Time

		daoMock *daoMock

		expectErr   error
		expectEvent string
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

			expectErr:   errFoo,
			expectEvent: "sweep",
		},
		{
			name: "Error/Panic",

			daoMock: &daoMock{panics: true},

			expectEvent: "recovered panic",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			transport := sentryftest.Record(t)

			daoSweep := coremocks.NewMockGenerationSweepDao(t)
			serviceCheck := coremocks.NewMockGenerationSweepServiceCheck(t)

			if testCase.daoMock != nil {
				call := daoSweep.EXPECT().
					Exec(mock.Anything, &dao.GenerationSweepRequest{Interval: sweep.Interval / 2, Limit: sweep.BatchSize})

				if testCase.daoMock.panics {
					call.Panic(errFoo.Error())
				} else {
					call.Return(nil, testCase.daoMock.err)
				}
			}

			service := core.NewGenerationSweep(daoSweep, serviceCheck, sweep)

			worked, err := sweepRun(service, testCase.downtimeStart)(t.Context())
			require.ErrorIs(t, err, testCase.expectErr)
			require.False(t, worked)

			events := transport.Events()

			if testCase.expectEvent == "" {
				require.Empty(t, events)
			} else {
				require.Len(t, events, 1)
				require.Equal(t, testCase.expectEvent, events[0].Exception[0].Value)
			}

			daoSweep.AssertExpectations(t)
			serviceCheck.AssertExpectations(t)
		})
	}
}
