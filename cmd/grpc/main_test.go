package main

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/lib/connectors"
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

// TestNewConnectors checks the connectors the server builds against the Async mode's requirements in
// docs/providers.md. It calls the provider and costs money, so it runs only when PROVIDER_CONFORMANCE
// is true. Run it before changing a connector or the account it reaches.
func TestNewConnectors(t *testing.T) {
	t.Parallel()

	if os.Getenv("PROVIDER_CONFORMANCE") != "true" {
		t.Skip("calls the configured provider; set PROVIDER_CONFORMANCE=true to run it")
	}

	registry, err := connectors.NewRegistry(connectors.OpenAIConfig{
		BaseURL: config.AppPresetDefault.OpenAI.BaseURL, APIKey: config.AppPresetDefault.OpenAI.APIKey,
	})
	require.NoError(t, err)

	connector, err := registry.Async("openai")
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

	await := func(t *testing.T, id string) *connectors.Call {
		t.Helper()

		for {
			call, err := connector.Get(t.Context(), id)
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

		started, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{Payload: payload("gpt-5.6-luna", "low")})
		require.NoError(t, err)
		// Background mode, with the caller's store: false: the id comes back before the model runs.
		require.NotEmpty(t, started.ID)
		require.False(t, started.State.Terminal())

		finished := await(t, started.ID)
		require.Equal(t, connectors.CallSucceeded, finished.State, finished.Reason)
		require.True(t, json.Valid(finished.Response))

		// The usage record needs what ran and what it consumed, on every answered call.
		require.NotEmpty(t, finished.Model)
		require.NotEmpty(t, finished.Class)
		require.False(t, finished.StartedAt.IsZero())
		require.NotNil(t, finished.Usage)
		require.Positive(t, finished.Usage.OutputTokens)

		// Re-attach: the sweep reads an unpolled result within one interval, so it must outlive one.
		time.Sleep(config.SweepIntervalCeiling)

		again, err := connector.Get(t.Context(), started.ID)
		require.NoError(t, err, "result gone %s after it finished", config.SweepIntervalCeiling)
		require.JSONEq(t, output(t, finished.Response), output(t, again.Response))
	})

	// Streaming mode: the start waits for the provider to begin the call, then a stream reads its
	// events, as sent, through the terminal one.
	t.Run("Success/Streamed", func(t *testing.T) {
		t.Parallel()

		streaming, err := registry.Streaming("openai")
		require.NoError(t, err)

		started, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{
			Payload: payload("gpt-5.6-luna", "low"), Stream: true,
		})
		require.NoError(t, err)
		require.NotEmpty(t, started.ID)
		// Left only once the provider began it: a call left queued can stay queued for good.
		require.NotEqual(t, connectors.CallQueued, started.State)

		var last *connectors.StreamEvent

		for event, err := range streaming.Stream(t.Context(), &connectors.StreamRequest{ID: started.ID}) {
			require.NoError(t, err)
			require.True(t, json.Valid(event.Data))

			if last != nil {
				require.Greater(t, event.Sequence, last.Sequence)
			}

			last = event
		}

		require.NotNil(t, last)
		require.True(t, last.Terminal)
	})

	t.Run("Success/CancelFinished", func(t *testing.T) {
		t.Parallel()

		// A cancel that arrives after the call finished must still return the result: the provider
		// refuses to cancel it, and the connector reads it instead.
		started, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{Payload: payload("gpt-5.6-luna", "low")})
		require.NoError(t, err)

		finished := await(t, started.ID)
		require.Equal(t, connectors.CallSucceeded, finished.State, finished.Reason)

		cancelled, err := connector.Cancel(t.Context(), started.ID)
		require.NoError(t, err)
		require.Equal(t, connectors.CallSucceeded, cancelled.State)
		require.JSONEq(t, output(t, finished.Response), output(t, cancelled.Response))
	})

	t.Run("Success/Cancel", func(t *testing.T) {
		t.Parallel()

		// A slow model and effort, so the cancel lands while the call still runs.
		started, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{Payload: payload("gpt-5.6-sol", "high")})
		require.NoError(t, err)

		_, err = connector.Cancel(t.Context(), started.ID)
		require.NoError(t, err)
		require.Equal(t, connectors.CallCancelled, await(t, started.ID).State)
	})

	t.Run("Error/Rejected", func(t *testing.T) {
		t.Parallel()

		// A request the provider refuses fails before anything runs, with the provider's reason.
		_, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{Payload: payload("no-such-model", "low")})

		var rejection *connectors.RejectionError

		require.ErrorAs(t, err, &rejection)
		require.True(t, json.Valid(rejection.Response))
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
