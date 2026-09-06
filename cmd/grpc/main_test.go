package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type discardLog struct{}

func (discardLog) Info(context.Context, string, ...any) {}

func (discardLog) Warn(context.Context, string, ...any) {}

func (discardLog) Err(context.Context, string, ...any) {}

func TestRunProcess(t *testing.T) {
	t.Parallel()

	const privatePanicPayload = "private provider response"

	testCases := []struct {
		name          string
		failure       func(context.Context) error
		expectedError string
	}{
		{
			name: "WorkerPanic",
			failure: func(ctx context.Context) error {
				return observeLoop(ctx, "generation-worker", func() {
					panic(privatePanicPayload)
				})
			},
			expectedError: "generation-worker: background loop panicked",
		},
		{
			name: "UnexpectedReaperReturn",
			failure: func(ctx context.Context) error {
				return observeLoop(ctx, "generation-reaper", func() {})
			},
			expectedError: "generation-reaper: background loop exited unexpectedly",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()

			var (
				unavailable atomic.Bool
				started     sync.WaitGroup
			)
			started.Add(2)

			var serverCancelledAfterUnavailable atomic.Bool

			server := func(ctx context.Context) error {
				started.Done()
				<-ctx.Done()
				serverCancelledAfterUnavailable.Store(unavailable.Load())

				return nil
			}

			var siblingCancelledAfterUnavailable atomic.Bool

			sibling := func(ctx context.Context) error {
				started.Done()
				<-ctx.Done()
				siblingCancelledAfterUnavailable.Store(unavailable.Load())

				return nil
			}

			failure := func(ctx context.Context) error {
				started.Wait()

				return testCase.failure(ctx)
			}

			beginShutdown := sync.OnceFunc(func() {
				unavailable.Store(true)
				cancel()
			})

			err := runProcess(ctx, beginShutdown, server, sibling, failure)

			require.ErrorContains(t, err, testCase.expectedError)
			require.NotContains(t, err.Error(), privatePanicPayload)
			require.True(t, serverCancelledAfterUnavailable.Load())
			require.True(t, siblingCancelledAfterUnavailable.Load())
		})
	}
}

func TestRunProcessNormalCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var started sync.WaitGroup
	started.Add(3)

	component := func(ctx context.Context) error {
		started.Done()
		<-ctx.Done()

		return nil
	}

	go func() {
		started.Wait()
		cancel()
	}()

	var shutdowns atomic.Int32

	beginShutdown := sync.OnceFunc(func() {
		shutdowns.Add(1)
		cancel()
	})

	err := runProcess(ctx, beginShutdown, component, component, component)

	require.NoError(t, err)
	require.EqualValues(t, 1, shutdowns.Load())
}

func TestRunLoopContinuesAfterUnitFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var calls atomic.Int32

	err := runLoop(
		ctx,
		discardLog{},
		"generation-worker",
		time.Nanosecond,
		0,
		func(context.Context) (bool, error) {
			if calls.Add(1) == 3 {
				cancel()
			}

			return false, errors.New("provider failed")
		},
	)

	require.NoError(t, err)
	require.GreaterOrEqual(t, calls.Load(), int32(3))
}
