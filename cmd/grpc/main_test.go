package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/lib"
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
		"generation-sweep",
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

// TestNewAdapters checks the adapters the server builds against the Async mode's requirements in
// docs/providers.md. It calls the provider and costs money, so it runs only when PROVIDER_CONFORMANCE
// is true. Run it before changing an adapter or the account it reaches.
func TestNewAdapters(t *testing.T) {
	t.Parallel()

	if os.Getenv("PROVIDER_CONFORMANCE") != "true" {
		t.Skip("calls the configured provider; set PROVIDER_CONFORMANCE=true to run it")
	}

	adapters, err := newAdapters(config.AppPresetDefault)
	require.NoError(t, err)

	adapter, err := adapters.Async("openai")
	require.NoError(t, err)

	// payload is what a caller sends: its own model, effort, schema and privacy fields. The schema
	// exercises the strict subset callers rely on: nesting, an array, an enum and a nullable field.
	payload := func(model, effort string) json.RawMessage {
		return json.RawMessage(`{
			"model": "` + model + `",
			"reasoning": {"effort": "` + effort + `"},
			"max_output_tokens": 4096,
			"store": false,
			"instructions": "Outline the scene in the input as two short beats. Leave the note empty.",
			"input": "{\"scene\": \"a door left ajar at night\"}",
			"text": {"format": {"type": "json_schema", "name": "outline", "strict": true, "schema": {
				"type": "object",
				"properties": {
					"title": {"type": "string"},
					"mood": {"type": "string", "enum": ["calm", "tense"]},
					"beats": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {"text": {"type": "string"}},
							"required": ["text"],
							"additionalProperties": false
						}
					},
					"note": {"type": ["string", "null"]}
				},
				"required": ["title", "mood", "beats", "note"],
				"additionalProperties": false
			}}}
		}`)
	}

	await := func(t *testing.T, id string) *lib.ProviderCall {
		t.Helper()

		for {
			call, err := adapter.Get(t.Context(), id)
			require.NoError(t, err)

			if call.State.Terminal() {
				return call
			}

			time.Sleep(2 * time.Second)
		}
	}

	// output reads the part of a response that must survive a re-read unchanged.
	output := func(t *testing.T, response json.RawMessage) string {
		t.Helper()

		var body struct {
			Output json.RawMessage `json:"output"`
		}

		require.NoError(t, json.Unmarshal(response, &body))
		require.NotEmpty(t, body.Output)

		return string(body.Output)
	}

	t.Run("Success", func(t *testing.T) {
		t.Parallel()

		started, err := adapter.Start(t.Context(), &lib.AsyncStartRequest{Payload: payload("gpt-5.6-luna", "low")})
		require.NoError(t, err)
		// Background mode, with the caller's store: false: the id comes back before the model runs.
		require.NotEmpty(t, started.ID)
		require.Equal(t, lib.ProviderCallRunning, started.State)

		finished := await(t, started.ID)
		require.Equal(t, lib.ProviderCallSucceeded, finished.State, finished.Reason)
		require.True(t, json.Valid(finished.Response))

		// Re-attach: the sweep reads an unpolled result within one interval, so it must outlive one.
		time.Sleep(core.SweepIntervalCeiling)

		again, err := adapter.Get(t.Context(), started.ID)
		require.NoError(t, err, "result gone %s after it finished", core.SweepIntervalCeiling)
		require.JSONEq(t, output(t, finished.Response), output(t, again.Response))
	})

	t.Run("Success/CancelFinished", func(t *testing.T) {
		t.Parallel()

		// A cancel that arrives after the call finished must still return the result: the provider
		// refuses to cancel it, and the adapter reads it instead.
		started, err := adapter.Start(t.Context(), &lib.AsyncStartRequest{Payload: payload("gpt-5.6-luna", "low")})
		require.NoError(t, err)

		finished := await(t, started.ID)
		require.Equal(t, lib.ProviderCallSucceeded, finished.State, finished.Reason)

		cancelled, err := adapter.Cancel(t.Context(), started.ID)
		require.NoError(t, err)
		require.Equal(t, lib.ProviderCallSucceeded, cancelled.State)
		require.JSONEq(t, output(t, finished.Response), output(t, cancelled.Response))
	})

	t.Run("Success/Cancel", func(t *testing.T) {
		t.Parallel()

		// A slow model and effort, so the cancel lands while the call still runs.
		started, err := adapter.Start(t.Context(), &lib.AsyncStartRequest{Payload: payload("gpt-5.6-sol", "high")})
		require.NoError(t, err)

		_, err = adapter.Cancel(t.Context(), started.ID)
		require.NoError(t, err)
		require.Equal(t, lib.ProviderCallCancelled, await(t, started.ID).State)
	})

	t.Run("Error/Rejected", func(t *testing.T) {
		t.Parallel()

		// A request the provider refuses fails before anything runs, with the provider's reason.
		_, err := adapter.Start(t.Context(), &lib.AsyncStartRequest{Payload: payload("no-such-model", "low")})

		var rejection *lib.ProviderRejectionError

		require.ErrorAs(t, err, &rejection)
		require.True(t, json.Valid(rejection.Response))
	})
}

func TestOutsideDowntime(t *testing.T) {
	t.Parallel()

	started := time.Now().Add(-time.Hour)
	scheduled := time.Now().Add(time.Hour)

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
			name:      "Scheduled",
			start:     &scheduled,
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
