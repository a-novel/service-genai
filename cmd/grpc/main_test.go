package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunProcess(t *testing.T) {
	t.Parallel()

	const privatePanicPayload = "private provider response"

	testCases := []struct {
		name          string
		failure       func(context.Context) error
		expectedError string
	}{
		{
			name: "LoopPanic",
			failure: func(ctx context.Context) error {
				return observeLoop(ctx, "generation-sweep", func() {
					panic(privatePanicPayload)
				})
			},
			expectedError: "generation-sweep: background loop panicked",
		},
		{
			name: "UnexpectedLoopReturn",
			failure: func(ctx context.Context) error {
				return observeLoop(ctx, "generation-sweep", func() {})
			},
			expectedError: "generation-sweep: background loop exited unexpectedly",
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

	t.Run("NormalCancellation", func(t *testing.T) {
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
	})
}

func TestOutsideDowntime(t *testing.T) {
	t.Parallel()

	started := time.Now().Add(-time.Hour)

	testCases := []struct {
		name  string
		start *time.Time

		expectRun bool
	}{
		{
			name:      "NoDowntime",
			expectRun: true,
		},
		{
			name:  "Started",
			start: &started,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ran := false

			busy, err := outsideDowntime(testCase.start, func(context.Context) (bool, error) {
				ran = true

				return true, nil
			})(t.Context())

			require.NoError(t, err)
			require.Equal(t, testCase.expectRun, ran)
			require.Equal(t, testCase.expectRun, busy)
		})
	}
}
