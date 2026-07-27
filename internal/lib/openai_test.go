package lib_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/lib"
)

// scriptedProvider stands in for the Responses API. It records the last request body so the
// pass-through assertions can read it, and answers with whatever the case scripted.
type scriptedProvider struct {
	status int
	body   string

	lastPath string
	lastBody map[string]any
}

func (script *scriptedProvider) serve(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		script.lastPath = request.URL.Path

		if request.Body != nil {
			_ = json.NewDecoder(request.Body).Decode(&script.lastBody)
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(script.status)
		_, _ = writer.Write([]byte(script.body))
	}))

	t.Cleanup(server.Close)

	return server
}

func newTestProvider(t *testing.T, script *scriptedProvider) *lib.OpenAI {
	t.Helper()

	server := script.serve(t)

	// MaxRetries(0) so a scripted 5xx surfaces as one classified error instead of the SDK's own
	// retry loop deciding first.
	return lib.NewOpenAI(
		option.WithBaseURL(server.URL),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
}

const (
	responseCompleted = `{
		"id": "resp_1",
		"status": "completed",
		"model": "gpt-5.6-terra-2026-01-01",
		"usage": {
			"input_tokens": 1000,
			"input_tokens_details": {"cached_tokens": 200},
			"output_tokens": 500,
			"output_tokens_details": {"reasoning_tokens": 100},
			"total_tokens": 1500
		}
	}`
	responseQueued     = `{"id": "resp_1", "status": "queued", "model": "gpt-5.6-terra"}`
	responseIncomplete = `{
		"id": "resp_1",
		"status": "incomplete",
		"model": "gpt-5.6-terra",
		"incomplete_details": {"reason": "max_output_tokens"},
		"usage": {
			"input_tokens": 10,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens": 4096,
			"output_tokens_details": {"reasoning_tokens": 0},
			"total_tokens": 4106
		}
	}`
	responseFailed = `{
		"id": "resp_1",
		"status": "failed",
		"model": "gpt-5.6-terra",
		"error": {"code": "server_error", "message": "the model failed"}
	}`
	responseCancelled = `{"id": "resp_1", "status": "cancelled", "model": "gpt-5.6-terra"}`
)

func TestOpenAI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		script *scriptedProvider

		expectState  lib.ProviderCallState
		expectModel  string
		expectReason string
		expectUsage  *lib.ProviderUsage
		expectErr    error
	}{
		{
			name: "Success/Completed",

			script: &scriptedProvider{status: http.StatusOK, body: responseCompleted},

			expectState: lib.ProviderCallSucceeded,
			// The snapshot the provider served, not the alias the caller asked for.
			expectModel: "gpt-5.6-terra-2026-01-01",
			expectUsage: &lib.ProviderUsage{
				InputTokens: 1000, CachedInputTokens: 200,
				OutputTokens: 500, ReasoningTokens: 100,
			},
		},
		{
			// Background mode answers before the model has run, which is the whole point: the
			// identifier exists to be recorded now and polled later.
			name: "Success/QueuedIsNotTerminal",

			script: &scriptedProvider{status: http.StatusOK, body: responseQueued},

			expectState: lib.ProviderCallRunning,
			expectModel: "gpt-5.6-terra",
		},
		{
			// Terminal, and it consumed tokens. Treating it as a failure would lose the usage.
			name: "Success/IncompleteCarriesUsage",

			script: &scriptedProvider{status: http.StatusOK, body: responseIncomplete},

			expectState:  lib.ProviderCallIncomplete,
			expectModel:  "gpt-5.6-terra",
			expectReason: "max_output_tokens",
			expectUsage: &lib.ProviderUsage{
				InputTokens: 10, OutputTokens: 4096, ReasoningTokens: 0,
			},
		},
		{
			name: "Success/Failed",

			script: &scriptedProvider{status: http.StatusOK, body: responseFailed},

			expectState:  lib.ProviderCallFailed,
			expectModel:  "gpt-5.6-terra",
			expectReason: "the model failed",
		},
		{
			name: "Success/Cancelled",

			script: &scriptedProvider{status: http.StatusOK, body: responseCancelled},

			expectState: lib.ProviderCallCancelled,
			expectModel: "gpt-5.6-terra",
		},
		{
			// The request was fine and may succeed unchanged, so another attempt is worth spending.
			name: "Error/Retryable/RateLimited",

			script: &scriptedProvider{status: http.StatusTooManyRequests, body: `{"error":{"message":"slow down"}}`},

			expectErr: lib.ErrProviderRetryable,
		},
		{
			name: "Error/Retryable/ProviderFault",

			script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

			expectErr: lib.ErrProviderRetryable,
		},
		{
			// Terminal: retrying only spends an attempt to be rejected again.
			name: "Error/Terminal/BadRequest",

			script: &scriptedProvider{status: http.StatusBadRequest, body: `{"error":{"message":"unknown model"}}`},
		},
		{
			name: "Error/Terminal/Unauthorized",

			script: &scriptedProvider{status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := newTestProvider(t, testCase.script)

			call, err := provider.Start(t.Context(), &lib.ProviderStartRequest{
				Request:      json.RawMessage(`{"model": "gpt-5.6-terra", "input": "write"}`),
				GenerationID: "01999999-0000-7000-8000-000000000001",
				Attempt:      1,
			})

			if testCase.expectState == "" {
				require.Error(t, err)

				if testCase.expectErr != nil {
					require.ErrorIs(t, err, testCase.expectErr)
				} else {
					// Terminal failures must not be mistaken for retryable ones, or a rejected
					// request burns every attempt it has.
					require.NotErrorIs(t, err, lib.ErrProviderRetryable)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, "resp_1", call.ID)
			require.Equal(t, testCase.expectState, call.State)
			require.Equal(t, testCase.expectModel, call.Model)
			require.Equal(t, testCase.expectReason, call.Reason)
			require.Equal(t, testCase.expectUsage, call.Usage)
			require.Equal(t, testCase.expectState != lib.ProviderCallRunning, call.State.Terminal())
			// The output is the provider's response verbatim, so the caller reads whatever it asked
			// for without this service knowing the shape.
			require.NotEmpty(t, call.Output)
		})
	}
}

// The pass-through is the contract: the caller owns every parameter, and this service adds only the
// two fields crash-safety needs.
func TestOpenAIRequestPassThrough(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		request json.RawMessage

		// expectKept are caller fields that must reach the provider untouched, including ones this
		// SDK version has no typed parameter for.
		expectKept map[string]any
	}{
		{
			name: "KeepsEveryCallerField",

			request: json.RawMessage(`{
				"model": "gpt-5.6-terra",
				"input": "write",
				"max_output_tokens": 4096,
				"reasoning": {"effort": "high"}
			}`),

			expectKept: map[string]any{
				"model":             "gpt-5.6-terra",
				"input":             "write",
				"max_output_tokens": float64(4096),
			},
		},
		{
			// A field this SDK has never heard of must survive. Decoding into typed parameters
			// would drop it, which is why the request is forwarded as raw JSON.
			name: "KeepsAFieldTheSdkDoesNotKnow",

			request: json.RawMessage(`{"model": "gpt-5.6-terra", "some_future_knob": "on"}`),

			expectKept: map[string]any{"some_future_knob": "on"},
		},
		{
			// Crash-safety is not the caller's to disable.
			name: "OverridesBackgroundEvenWhenTheCallerSetIt",

			request: json.RawMessage(`{"model": "gpt-5.6-terra", "background": false}`),

			expectKept: map[string]any{"model": "gpt-5.6-terra"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			script := &scriptedProvider{status: http.StatusOK, body: responseQueued}
			provider := newTestProvider(t, script)

			_, err := provider.Start(t.Context(), &lib.ProviderStartRequest{
				Request:      testCase.request,
				GenerationID: "01999999-0000-7000-8000-000000000001",
				Attempt:      2,
			})
			require.NoError(t, err)

			for key, value := range testCase.expectKept {
				require.Equal(t, value, script.lastBody[key], "field %q", key)
			}

			require.Equal(t, true, script.lastBody["background"])
			require.Equal(t, map[string]any{
				"generation_id": "01999999-0000-7000-8000-000000000001",
				"attempt":       "2",
			}, script.lastBody["metadata"])
		})
	}
}

// Get is both the poll and the re-attach. A generation recovered from a crash calls it and resumes
// the operation already paid for.
func TestOpenAIGet(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		script *scriptedProvider

		expectState lib.ProviderCallState
		expectErr   error
	}{
		{
			name: "Success/StillRunning",

			script: &scriptedProvider{status: http.StatusOK, body: responseQueued},

			expectState: lib.ProviderCallRunning,
		},
		{
			name: "Success/Completed",

			script: &scriptedProvider{status: http.StatusOK, body: responseCompleted},

			expectState: lib.ProviderCallSucceeded,
		},
		{
			// The operation is gone, so there is nothing to re-attach to. Terminal, not retryable:
			// polling again cannot bring it back.
			name: "Error/Terminal/NotFound",

			script: &scriptedProvider{status: http.StatusNotFound, body: `{"error":{"message":"no such response"}}`},
		},
		{
			name: "Error/Retryable/ProviderFault",

			script: &scriptedProvider{status: http.StatusServiceUnavailable, body: `{"error":{"message":"unavailable"}}`},

			expectErr: lib.ErrProviderRetryable,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := newTestProvider(t, testCase.script)

			call, err := provider.Get(t.Context(), "resp_1")

			if testCase.expectState == "" {
				require.Error(t, err)

				if testCase.expectErr != nil {
					require.ErrorIs(t, err, testCase.expectErr)
				} else {
					require.NotErrorIs(t, err, lib.ErrProviderRetryable)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, testCase.expectState, call.State)
			require.Contains(t, testCase.script.lastPath, "resp_1")
		})
	}
}

func TestOpenAICancel(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		script *scriptedProvider

		expectState lib.ProviderCallState
		expectErr   error
	}{
		{
			name: "Success",

			script: &scriptedProvider{status: http.StatusOK, body: responseCancelled},

			expectState: lib.ProviderCallCancelled,
		},
		{
			// Cancelling twice returns the final state rather than failing, so a worker racing the
			// provider does not need to care which got there first.
			name: "Success/AlreadyTerminal",

			script: &scriptedProvider{status: http.StatusOK, body: responseCompleted},

			expectState: lib.ProviderCallSucceeded,
		},
		{
			name: "Error/Retryable/ProviderFault",

			script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

			expectErr: lib.ErrProviderRetryable,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := newTestProvider(t, testCase.script)

			call, err := provider.Cancel(t.Context(), "resp_1")

			if testCase.expectState == "" {
				require.ErrorIs(t, err, testCase.expectErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testCase.expectState, call.State)
		})
	}
}

// A dial failure has no HTTP response to classify, and it is exactly the case where another attempt
// is most likely to succeed.
func TestOpenAITransportFailure(t *testing.T) {
	t.Parallel()

	// A port nothing listens on.
	provider := lib.NewOpenAI(
		option.WithBaseURL("http://127.0.0.1:1"),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)

	_, err := provider.Start(t.Context(), &lib.ProviderStartRequest{
		Request:      json.RawMessage(`{"model": "gpt-5.6-terra"}`),
		GenerationID: "01999999-0000-7000-8000-000000000001",
		Attempt:      1,
	})
	require.ErrorIs(t, err, lib.ErrProviderRetryable)
}

// A request that is not a JSON object cannot have the two owned fields merged into it, and that is
// a caller bug rather than something to send and have rejected.
func TestOpenAIMalformedRequest(t *testing.T) {
	t.Parallel()

	script := &scriptedProvider{status: http.StatusOK, body: responseQueued}
	provider := newTestProvider(t, script)

	_, err := provider.Start(t.Context(), &lib.ProviderStartRequest{
		Request:      json.RawMessage(`"not an object"`),
		GenerationID: "01999999-0000-7000-8000-000000000001",
		Attempt:      1,
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, lib.ErrProviderRetryable)
}

// Name is what the usage record stores as the provider, so it is part of the contract rather than a
// label.
func TestOpenAIName(t *testing.T) {
	t.Parallel()

	provider := lib.NewOpenAI(option.WithAPIKey("test-key"))
	require.Equal(t, lib.ProviderNameOpenAI, provider.Name())
}

// A status this adapter has never seen is treated as still running, not as terminal. Polling again
// is cheap; settling on a state we do not understand throws away work already paid for.
func TestOpenAIUnknownStatus(t *testing.T) {
	t.Parallel()

	script := &scriptedProvider{
		status: http.StatusOK,
		body:   `{"id": "resp_1", "status": "something_new", "model": "gpt-5.6-terra"}`,
	}
	provider := newTestProvider(t, script)

	call, err := provider.Get(t.Context(), "resp_1")
	require.NoError(t, err)
	require.Equal(t, lib.ProviderCallRunning, call.State)
	require.False(t, call.State.Terminal())
}
