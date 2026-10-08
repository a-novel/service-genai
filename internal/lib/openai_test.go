package lib_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/lib"
)

// scriptedProvider stands in for the Responses API. It records the last request so the composition
// assertions can read it, and answers with whatever the case scripted.
type scriptedProvider struct {
	status int
	body   string

	lastPath string
	lastBody json.RawMessage
}

func (script *scriptedProvider) serve(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		script.lastPath = request.URL.Path

		if request.Body != nil {
			script.lastBody, _ = io.ReadAll(request.Body)
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(script.status)
		_, _ = writer.Write([]byte(script.body))
	}))

	t.Cleanup(server.Close)

	return server
}

var testTiers = map[lib.Tier]lib.TierBinding{
	lib.TierFast:     {Model: "gpt-5.6-luna", ReasoningEffort: "low", MaxOutputTokens: 1024},
	lib.TierBalanced: {Model: "gpt-5.6-terra", ReasoningEffort: "medium", MaxOutputTokens: 2048},
	lib.TierDeep:     {Model: "gpt-5.6-sol", MaxOutputTokens: 4096},
}

func newTestProvider(t *testing.T, script *scriptedProvider) *lib.OpenAI {
	t.Helper()

	server := script.serve(t)

	// MaxRetries(0) so a scripted 5xx surfaces as one classified error instead of the SDK's own
	// retry loop deciding first.
	provider, err := lib.NewOpenAI(
		testTiers,
		option.WithBaseURL(server.URL),
		option.WithHTTPClient(server.Client()),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	if err != nil {
		panic(err)
	}

	return provider
}

func testStartRequest(tier lib.Tier) *lib.ProviderStartRequest {
	return &lib.ProviderStartRequest{
		Tier:         tier,
		Instructions: "Continue the scene.",
		Input:        json.RawMessage(`{"scene": "a door"}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"string"}}}`),
		EndUserID:    "00000000-0000-0000-0000-000000000001",
		GenerationID: "01999999-0000-7000-8000-000000000001",
		Attempt:      1,
	}
}

// response builds a Responses API body around the given status and extra fields.
func response(status string, extra string) string {
	body := `{"id": "resp_1", "status": "` + status + `", "model": "gpt-5.6-terra-2026-01-01",` +
		` "reasoning": {"effort": "medium"}`
	if extra != "" {
		body += ", " + extra
	}

	return body + "}"
}

const (
	testUsage = `"usage": {
		"input_tokens": 1000, "input_tokens_details": {"cached_tokens": 200},
		"output_tokens": 500, "output_tokens_details": {"reasoning_tokens": 100}, "total_tokens": 1500
	}`
	testOutput  = `"output": [{"type": "message", "content": [{"type": "output_text", "text": "{\"text\": \"done\"}"}]}]`
	testRefusal = `"output": [{"type": "message", "content": [{"type": "refusal", "refusal": "no"}]}]`
	testProse   = `"output": [{"type": "message", "content": [{"type": "output_text", "text": "not json"}]}]`
)

func TestOpenAI(t *testing.T) {
	t.Parallel()

	fullUsage := &lib.ProviderUsage{InputTokens: 1000, CachedInputTokens: 200, OutputTokens: 500, ReasoningTokens: 100}

	testCases := []struct {
		name string

		script *scriptedProvider

		expectState     lib.ProviderCallState
		expectOutput    string
		expectUsage     *lib.ProviderUsage
		expectFailure   *lib.Failure
		expectRetryable bool
		expectErr       error
		// expectRejection is the failure a definitive start rejection carries.
		expectRejection *lib.Failure
	}{
		{
			// The output is the schema document itself, not the provider's envelope.
			name: "Success/Completed",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", testOutput+", "+testUsage)},

			expectState:  lib.ProviderCallSucceeded,
			expectOutput: `{"text": "done"}`,
			expectUsage:  fullUsage,
		},
		{
			// Background mode answers before the model has run: the identifier is recorded now and
			// polled later.
			name: "Success/QueuedIsNotTerminal",

			script: &scriptedProvider{status: http.StatusOK, body: response("queued", "")},

			expectState: lib.ProviderCallRunning,
		},
		{
			name: "Success/Refused",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", testRefusal+", "+testUsage)},

			expectState:   lib.ProviderCallFailed,
			expectUsage:   fullUsage,
			expectFailure: &lib.Failure{Kind: lib.FailureRefused, Message: "the model refused the request"},
		},
		{
			name: "Success/OutputNotAnObject",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", testProse+", "+testUsage)},

			expectState: lib.ProviderCallFailed,
			expectUsage: fullUsage,
			expectFailure: &lib.Failure{
				Kind: lib.FailureFailed, Message: "the provider returned an output that is not a JSON object",
			},
		},
		{
			// Terminal, and it consumed tokens: the usage is kept.
			name: "Success/IncompleteAtCeiling",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"incomplete", `"incomplete_details": {"reason": "max_output_tokens"}, `+testUsage,
			)},

			expectState: lib.ProviderCallFailed,
			expectUsage: fullUsage,
			expectFailure: &lib.Failure{
				Kind: lib.FailureIncomplete, Message: "the output reached the tier's output token ceiling",
			},
		},
		{
			name: "Success/IncompleteContentFilter",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"incomplete", `"incomplete_details": {"reason": "content_filter"}`,
			)},

			expectState: lib.ProviderCallFailed,
			expectFailure: &lib.Failure{
				Kind: lib.FailureRefused, Message: "the provider's content filter stopped the output",
			},
		},
		{
			name: "Success/RetryableFailure",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"failed", `"error": {"code": "server_error", "message": "the model failed"}`,
			)},

			expectState:     lib.ProviderCallFailed,
			expectFailure:   &lib.Failure{Kind: lib.FailureFailed, Message: "the provider failed to complete the call"},
			expectRetryable: true,
		},
		{
			name: "Success/PolicyFailure",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"failed", `"error": {"code": "invalid_prompt", "message": "the prompt was rejected"}`,
			)},

			expectState: lib.ProviderCallFailed,
			expectFailure: &lib.Failure{
				Kind: lib.FailureRefused, Message: "the provider's usage policy refused the request",
			},
		},
		{
			name: "Success/Cancelled",

			script: &scriptedProvider{status: http.StatusOK, body: response("cancelled", "")},

			expectState: lib.ProviderCallCancelled,
		},
		{
			// Nothing was accepted, so another start costs nothing.
			name: "Error/RateLimited",

			script: &scriptedProvider{status: http.StatusTooManyRequests, body: `{"error":{"message":"slow down"}}`},

			expectErr: lib.ErrProviderRetryable,
		},
		{
			name: "Error/ProviderFaultIsAmbiguous",

			script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

			expectErr: lib.ErrProviderStartAmbiguous,
		},
		{
			name: "Error/ContextExceeded",

			script: &scriptedProvider{
				status: http.StatusBadRequest,
				body:   `{"error":{"message":"too long","code":"context_length_exceeded"}}`,
			},

			expectRejection: &lib.Failure{
				Kind: lib.FailureInvalidRequest, Message: "the input exceeds the model's context window",
			},
		},
		{
			name: "Error/SchemaRejected",

			script: &scriptedProvider{
				status: http.StatusBadRequest,
				body:   `{"error":{"message":"bad schema","param":"text.format.schema"}}`,
			},

			expectRejection: &lib.Failure{
				Kind: lib.FailureInvalidRequest, Message: "the provider rejected the output schema",
			},
		},
		{
			name: "Error/BadRequest",

			script: &scriptedProvider{status: http.StatusBadRequest, body: `{"error":{"message":"nope"}}`},

			expectRejection: &lib.Failure{Kind: lib.FailureInvalidRequest, Message: "the provider rejected the request"},
		},
		{
			// The service's credentials, not the caller's request: fixing the request would not help.
			name: "Error/Unauthorized",

			script: &scriptedProvider{status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`},

			expectRejection: &lib.Failure{Kind: lib.FailureFailed, Message: "the provider refused the call"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := newTestProvider(t, testCase.script)

			call, err := provider.Start(t.Context(), testStartRequest(lib.TierBalanced))

			if testCase.expectRejection != nil {
				var rejection *lib.ProviderRejectionError

				require.ErrorAs(t, err, &rejection)
				require.Equal(t, *testCase.expectRejection, rejection.Failure)
				require.NotErrorIs(t, err, lib.ErrProviderRetryable)

				return
			}

			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				return
			}

			require.Equal(t, "resp_1", call.ID)
			require.Equal(t, testCase.expectState, call.State)
			// What the provider served, not what the Tier asked for.
			require.Equal(t, "gpt-5.6-terra-2026-01-01", call.Model)
			require.Equal(t, "medium", call.ReasoningEffort)
			require.Equal(t, testCase.expectUsage, call.Usage)
			require.Equal(t, testCase.expectFailure, call.Failure)
			require.Equal(t, testCase.expectRetryable, call.Retryable)

			if testCase.expectOutput != "" {
				require.JSONEq(t, testCase.expectOutput, string(call.Output))
			} else {
				require.Empty(t, call.Output)
			}
		})
	}
}

// The request is composed here from the Tier's binding: the caller never names a model, and the
// fields crash-safety and privacy depend on are always set.
func TestOpenAIRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		tier lib.Tier

		expectModel     string
		expectReasoning any
		expectMaxOutput float64
	}{
		{
			name: "Success/Balanced",

			tier: lib.TierBalanced,

			expectModel:     "gpt-5.6-terra",
			expectReasoning: map[string]any{"effort": "medium"},
			expectMaxOutput: 2048,
		},
		{
			// A binding without an effort sends none rather than an empty one the provider rejects.
			name: "Success/NoEffort",

			tier: lib.TierDeep,

			expectModel:     "gpt-5.6-sol",
			expectMaxOutput: 4096,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			script := &scriptedProvider{status: http.StatusOK, body: response("queued", "")}
			provider := newTestProvider(t, script)

			_, err := provider.Start(t.Context(), testStartRequest(testCase.tier))
			require.NoError(t, err)
			require.Equal(t, "/responses", script.lastPath)

			var sent map[string]any
			require.NoError(t, json.Unmarshal(script.lastBody, &sent))

			require.Equal(t, testCase.expectModel, sent["model"])
			require.Equal(t, testCase.expectReasoning, sent["reasoning"])
			require.InDelta(t, testCase.expectMaxOutput, sent["max_output_tokens"], 0)
			require.Equal(t, "Continue the scene.", sent["instructions"])
			require.JSONEq(t, `{"scene": "a door"}`, sent["input"].(string))
			require.Equal(t, true, sent["background"])
			require.Equal(t, false, sent["store"])
			require.Equal(t, map[string]any{
				"generation_id": "01999999-0000-7000-8000-000000000001", "attempt": "1",
			}, sent["metadata"])
			// Hashed: the provider attributes abuse without learning the platform's own identifier.
			require.Len(t, sent["safety_identifier"], 64)
			require.NotContains(t, sent["safety_identifier"], "00000000-0000-0000-0000-000000000001")

			// The schema reaches the provider as sent: its property order steers the output.
			require.Contains(t, string(script.lastBody),
				`"schema":{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"string"}}}`)
			require.Contains(t, string(script.lastBody), `"strict":true`)
		})
	}
}

func TestNewOpenAI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		tiers map[lib.Tier]lib.TierBinding

		expectErr error
	}{
		{
			name: "Success",

			tiers: testTiers,
		},
		{
			name: "Error/TierUnbound",

			tiers: map[lib.Tier]lib.TierBinding{lib.TierFast: testTiers[lib.TierFast]},

			expectErr: lib.ErrTierNotBound,
		},
		{
			name: "Error/NoOutputCeiling",

			tiers: map[lib.Tier]lib.TierBinding{
				lib.TierFast:     {Model: "a-model"},
				lib.TierBalanced: testTiers[lib.TierBalanced],
				lib.TierDeep:     testTiers[lib.TierDeep],
			},

			expectErr: lib.ErrTierNotBound,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := lib.NewOpenAI(testCase.tiers)
			require.ErrorIs(t, err, testCase.expectErr)
		})
	}
}

func TestOpenAIGet(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		script *scriptedProvider

		expectState lib.ProviderCallState
		expectErr   error
	}{
		{
			name: "Success",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", testOutput)},

			expectState: lib.ProviderCallSucceeded,
		},
		{
			// Gone past the provider's retention window: not something another read fixes.
			name: "Error/NotFound",

			script: &scriptedProvider{status: http.StatusNotFound, body: `{"error":{"message":"no such response"}}`},
		},
		{
			name: "Error/Retryable",

			script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

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
			require.Equal(t, "/responses/resp_1", testCase.script.lastPath)
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

			script: &scriptedProvider{status: http.StatusOK, body: response("cancelled", "")},

			expectState: lib.ProviderCallCancelled,
		},
		{
			// Cancelling a finished operation returns its final state, so the result is still read.
			name: "Success/AlreadyCompleted",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", testOutput)},

			expectState: lib.ProviderCallSucceeded,
		},
		{
			name: "Error/Retryable",

			script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

			expectErr: lib.ErrProviderRetryable,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := newTestProvider(t, testCase.script)

			call, err := provider.Cancel(t.Context(), "resp_1")
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				return
			}

			require.Equal(t, testCase.expectState, call.State)
			require.Equal(t, "/responses/resp_1/cancel", testCase.script.lastPath)
		})
	}
}

// A start the provider may have received but never answered cannot be retried as a new one.
func TestOpenAIStartTimeoutIsAmbiguous(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32

	release := make(chan struct{})
	defer close(release)

	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		requests.Add(1)

		<-release
	}))
	t.Cleanup(server.Close)

	provider, err := lib.NewOpenAI(
		testTiers,
		option.WithBaseURL(server.URL),
		option.WithHTTPClient(server.Client()),
		option.WithAPIKey("test-key"),
		option.WithRequestTimeout(time.Second),
	)
	require.NoError(t, err)

	_, err = provider.Start(t.Context(), testStartRequest(lib.TierFast))
	require.ErrorIs(t, err, lib.ErrProviderStartAmbiguous)
	require.Equal(t, int32(1), requests.Load())
}

// A failed dial proves nothing reached the provider, so the start is safe to retry rather than
// settled as an unknown outcome.
func TestOpenAITransportFailure(t *testing.T) {
	t.Parallel()

	// A port nothing listens on.
	provider, err := lib.NewOpenAI(
		testTiers,
		option.WithBaseURL("https://127.0.0.1:1"),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	require.NoError(t, err)

	_, err = provider.Start(t.Context(), testStartRequest(lib.TierFast))
	require.ErrorIs(t, err, lib.ErrProviderRetryable)
}

func TestOpenAIName(t *testing.T) {
	t.Parallel()

	provider, err := lib.NewOpenAI(testTiers)
	require.NoError(t, err)
	require.Equal(t, lib.ProviderNameOpenAI, provider.Name())
}

// An unknown status is polled again rather than settled: settling on a state this service does not
// understand would throw away work already paid for.
func TestOpenAIUnknownStatus(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t, &scriptedProvider{status: http.StatusOK, body: response("thinking", "")})

	call, err := provider.Start(t.Context(), testStartRequest(lib.TierFast))
	require.NoError(t, err)
	require.Equal(t, lib.ProviderCallRunning, call.State)
	require.False(t, call.State.Terminal())
}
