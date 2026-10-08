package main

import (
	"bytes"
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

// TestNewProvider checks the provider the server is configured with against the requirements in
// docs/providers.md. It calls the provider and costs money, so it runs only when
// PROVIDER_CONFORMANCE is true. Run it before changing the provider or its Tier bindings.
func TestNewProvider(t *testing.T) {
	t.Parallel()

	if os.Getenv("PROVIDER_CONFORMANCE") != "true" {
		t.Skip("calls the configured provider; set PROVIDER_CONFORMANCE=true to run it")
	}

	cfg := config.AppPresetDefault.Provider

	provider, err := newProvider(cfg)
	require.NoError(t, err)

	// The schema exercises the strict subset callers rely on: nesting, an array, an enum and a
	// nullable field.
	request := func(tier lib.Tier) *lib.ProviderStartRequest {
		return &lib.ProviderStartRequest{
			Tier:         tier,
			Instructions: "Outline the scene in the input as two short beats. Leave the note empty.",
			Input:        json.RawMessage(`{"scene": "a door left ajar at night"}`),
			OutputSchema: json.RawMessage(`{
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
			}`),
			EndUserID:    "00000000-0000-0000-0000-000000000001",
			GenerationID: "conformance",
			Attempt:      1,
		}
	}

	await := func(t *testing.T, id string) *lib.ProviderCall {
		t.Helper()

		for {
			call, err := provider.Get(t.Context(), id)
			require.NoError(t, err)

			if call.State.Terminal() {
				return call
			}

			time.Sleep(2 * time.Second)
		}
	}

	for _, tier := range lib.Tiers {
		binding := cfg.Tiers[string(tier)]

		t.Run("Success/"+string(tier), func(t *testing.T) {
			t.Parallel()

			// A model that rejects its effort or output ceiling fails here, naming the binding.
			started, err := provider.Start(t.Context(), request(tier))
			require.NoError(t, err, "tier %s binds model %q, effort %q", tier, binding.Model, binding.ReasoningEffort)
			// Background mode: the id comes back before the model runs.
			require.NotEmpty(t, started.ID)
			require.Equal(t, lib.ProviderCallRunning, started.State)

			finished := await(t, started.ID)
			require.Equal(t, lib.ProviderCallSucceeded, finished.State, "tier %s: %s", tier, finished.Reason)
			require.NotNil(t, finished.Usage)
			require.Positive(t, finished.Usage.InputTokens)
			require.Positive(t, finished.Usage.OutputTokens)

			// Strict mode: every property and nothing else. The key order is the provider's: background
			// mode reorders a schema's properties.
			decoder := json.NewDecoder(bytes.NewReader(finished.Output))
			decoder.DisallowUnknownFields()

			var output struct {
				Title string `json:"title"`
				Mood  string `json:"mood"`
				Beats []struct {
					Text string `json:"text"`
				} `json:"beats"`
				Note *string `json:"note"`
			}

			require.NoError(t, decoder.Decode(&output))
			require.Contains(t, []string{"calm", "tense"}, output.Mood)
			require.Len(t, output.Beats, 2)

			// Re-attach: the sweep reads an unpolled result within one interval, so it must outlive one.
			time.Sleep(core.SweepIntervalCeiling)

			again, err := provider.Get(t.Context(), started.ID)
			require.NoError(t, err, "tier %s: result gone %s after it finished", tier, core.SweepIntervalCeiling)
			require.JSONEq(t, string(finished.Output), string(again.Output))
		})
	}

	t.Run("Success/CancelFinished", func(t *testing.T) {
		t.Parallel()

		// A cancel that arrives after the call finished must still return the result: the provider
		// refuses to cancel it, and the adapter reads it instead.
		started, err := provider.Start(t.Context(), request(lib.TierFast))
		require.NoError(t, err)

		finished := await(t, started.ID)
		require.Equal(t, lib.ProviderCallSucceeded, finished.State, finished.Reason)

		cancelled, err := provider.Cancel(t.Context(), started.ID)
		require.NoError(t, err)
		require.Equal(t, lib.ProviderCallSucceeded, cancelled.State)
		require.JSONEq(t, string(finished.Output), string(cancelled.Output))
	})

	t.Run("Success/Cancel", func(t *testing.T) {
		t.Parallel()

		// The slowest Tier, so the cancel lands while the call still runs.
		started, err := provider.Start(t.Context(), request(lib.TierDeep))
		require.NoError(t, err)

		_, err = provider.Cancel(t.Context(), started.ID)
		require.NoError(t, err)
		require.Equal(t, lib.ProviderCallCancelled, await(t, started.ID).State)
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
