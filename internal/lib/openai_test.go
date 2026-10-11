package lib_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/lib"
)

// scriptedProvider stands in for the Responses API. It records the last request so the composition
// assertions can read it, and answers with whatever the case scripted.
type scriptedProvider struct {
	status int
	body   string
	// cancel, when set, answers cancel requests instead.
	cancel *scriptedProvider

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

		answer := script
		if script.cancel != nil && strings.HasSuffix(request.URL.Path, "/cancel") {
			answer = script.cancel
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(answer.status)
		_, _ = writer.Write([]byte(answer.body))
	}))

	t.Cleanup(server.Close)

	return server
}

// testConfig points the adapter at the given endpoint.
func testConfig(baseURL string) lib.OpenAIConfig {
	return lib.OpenAIConfig{BaseURL: baseURL, APIKey: "test-key"}
}

func newTestProvider(t *testing.T, script *scriptedProvider) *lib.OpenAI {
	t.Helper()

	server := script.serve(t)

	// MaxRetries(0) so a scripted 5xx surfaces as one classified error instead of the SDK's own
	// retry loop deciding first.
	provider, err := lib.NewOpenAI(
		testConfig(server.URL), option.WithHTTPClient(server.Client()), option.WithMaxRetries(0),
	)
	if err != nil {
		panic(err)
	}

	return provider
}

// testPayload is a caller's request: its schema lists z before a, and its input holds an ampersand.
const testPayload = `{"model": "gpt-5.6-terra", "input": "a & b", ` +
	`"text": {"format": {"type": "json_schema", "schema": {"properties": {"z": {}, "a": {}}}}}}`

func testStartRequest() *lib.AsyncStartRequest {
	return &lib.AsyncStartRequest{Payload: json.RawMessage(testPayload)}
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

const testRefusal = `"output": [{"type": "message", "content": [{"type": "refusal", "refusal": "no"}]}]`

func TestOpenAI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		script *scriptedProvider

		expectState     lib.ProviderCallState
		expectReason    string
		expectRetryable bool
		expectErr       error
		// expectRejection marks a definitive start rejection; expectBody is the error body it carries.
		expectRejection bool
		expectBody      string
	}{
		{
			name: "Success/Completed",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", "")},

			expectState: lib.ProviderCallSucceeded,
		},
		{
			// Background mode answers before the model has run: the identifier is recorded now and
			// polled later.
			name: "Success/QueuedIsNotTerminal",

			script: &scriptedProvider{status: http.StatusOK, body: response("queued", "")},

			expectState: lib.ProviderCallRunning,
		},
		{
			// A refusal is an answer: the caller reads it from the response.
			name: "Success/Refused",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", testRefusal)},

			expectState: lib.ProviderCallSucceeded,
		},
		{
			// The output stopped at the caller's own max_output_tokens. Finished, and the caller's to read.
			name: "Success/Incomplete",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"incomplete", `"incomplete_details": {"reason": "max_output_tokens"}`,
			)},

			expectState: lib.ProviderCallSucceeded,
		},
		{
			name: "Success/RetryableFailure",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"failed", `"error": {"code": "server_error", "message": "the model failed"}`,
			)},

			expectState:     lib.ProviderCallFailed,
			expectReason:    "the model failed",
			expectRetryable: true,
		},
		{
			name: "Success/PolicyFailure",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"failed", `"error": {"code": "invalid_prompt", "message": "the prompt was rejected"}`,
			)},

			expectState:  lib.ProviderCallFailed,
			expectReason: "the prompt was rejected",
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
			// Retrying cannot help until an operator adds credit, so the generation fails with the body.
			name: "Error/QuotaExhausted",

			script: &scriptedProvider{
				status: http.StatusTooManyRequests,
				body:   `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`,
			},

			expectRejection: true,
			expectBody:      `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`,
		},
		{
			// The caller built the request, so the provider's reason is what tells it what to fix.
			name: "Error/BadRequest",

			script: &scriptedProvider{
				status: http.StatusBadRequest,
				body:   `{"error":{"message":"bad schema","param":"text.format.schema"}}`,
			},

			expectRejection: true,
			expectBody:      `{"error":{"message":"bad schema","param":"text.format.schema"}}`,
		},
		{
			name: "Error/Unauthorized",

			script: &scriptedProvider{status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`},

			expectRejection: true,
			expectBody:      `{"error":{"message":"bad key"}}`,
		},
		{
			// A proxy in front of the provider may answer with anything; nothing is passed on.
			name: "Error/NoErrorBody",

			script: &scriptedProvider{status: http.StatusForbidden, body: `forbidden`},

			expectRejection: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := newTestProvider(t, testCase.script)

			call, err := provider.Start(t.Context(), testStartRequest())

			if testCase.expectRejection {
				var rejection *lib.ProviderRejectionError

				require.ErrorAs(t, err, &rejection)
				require.NotErrorIs(t, err, lib.ErrProviderRetryable)

				if testCase.expectBody == "" {
					require.Empty(t, rejection.Response)

					return
				}

				require.JSONEq(t, testCase.expectBody, string(rejection.Response))

				// The provider's own explanation reaches server logs too.
				var body struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}

				require.NoError(t, json.Unmarshal([]byte(testCase.script.body), &body))
				require.ErrorContains(t, err, body.Error.Message)

				return
			}

			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				return
			}

			require.Equal(t, "resp_1", call.ID)
			require.Equal(t, testCase.expectState, call.State)
			require.Equal(t, testCase.expectReason, call.Reason)
			require.Equal(t, testCase.expectRetryable, call.Retryable)

			if !call.State.Terminal() {
				require.Empty(t, call.Response)

				return
			}

			// The provider's answer, as it sent it.
			require.Equal(t, testCase.script.body, string(call.Response))
		})
	}
}

// The adapter writes the fields the Async mode owns and forwards everything else as the caller wrote
// it.
func TestOpenAIRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		payload string
	}{
		{
			name: "Success",

			payload: testPayload,
		},
		{
			// A caller asking for a synchronous or streamed call through the Async mode still gets one
			// the service can recover.
			name: "Success/ModeFieldsReplaced",

			payload: `{"model": "gpt-5.6-terra", "input": "a & b", "background": false, "stream": true, ` +
				`"text": {"format": {"type": "json_schema", "schema": {"properties": {"z": {}, "a": {}}}}}}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			script := &scriptedProvider{status: http.StatusOK, body: response("queued", "")}
			provider := newTestProvider(t, script)

			_, err := provider.Start(t.Context(), &lib.AsyncStartRequest{Payload: json.RawMessage(testCase.payload)})
			require.NoError(t, err)
			require.Equal(t, "/responses", script.lastPath)

			var sent map[string]any
			require.NoError(t, json.Unmarshal(script.lastBody, &sent))

			require.Equal(t, map[string]any{
				"model":      "gpt-5.6-terra",
				"input":      "a & b",
				"background": true,
				"stream":     false,
				"text": map[string]any{"format": map[string]any{
					"type": "json_schema",
					"schema": map[string]any{
						"properties": map[string]any{"z": map[string]any{}, "a": map[string]any{}},
					},
				}},
			}, sent)

			// Nested values reach the provider as written: unescaped, and in the caller's order.
			require.Contains(t, string(script.lastBody), `"input":"a & b"`)
			require.Contains(t, string(script.lastBody), `"properties":{"z":{},"a":{}}`)
		})
	}
}

func TestNewOpenAI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		config lib.OpenAIConfig

		expectErr error
	}{
		{
			name: "Success",

			config: testConfig("https://provider.test/v1"),
		},
		{
			name: "Error/NoBaseURL",

			config: testConfig(""),

			expectErr: lib.ErrProviderNotConfigured,
		},
		{
			name: "Error/NoAPIKey",

			config: lib.OpenAIConfig{BaseURL: "https://provider.test/v1"},

			expectErr: lib.ErrProviderNotConfigured,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := lib.NewOpenAI(testCase.config)
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

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", "")},

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
		{
			// Refused credentials are this service's configuration, not the operation's fate: once the
			// key is fixed, the result is read again.
			name: "Error/Unauthorized",

			script: &scriptedProvider{status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`},

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
			require.Equal(t, testCase.script.body, string(call.Response))
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
		// expectPath is the last path requested, the cancel itself unless stated.
		expectPath string
		expectErr  error
	}{
		{
			name: "Success",

			script: &scriptedProvider{status: http.StatusOK, body: response("cancelled", "")},

			expectState: lib.ProviderCallCancelled,
		},
		{
			// OpenAI refuses to cancel a finished response; its result is read instead, so a paid
			// output is not mistaken for a lost one.
			name: "Success/AlreadyCompleted",

			script: &scriptedProvider{
				status: http.StatusOK,
				body:   response("completed", ""),
				cancel: &scriptedProvider{
					status: http.StatusBadRequest, body: `{"error":{"message":"Cannot cancel a completed response."}}`,
				},
			},

			expectState: lib.ProviderCallSucceeded,
			expectPath:  "/responses/resp_1",
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
			require.Equal(t, lo.CoalesceOrEmpty(testCase.expectPath, "/responses/resp_1/cancel"), testCase.script.lastPath)
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
		testConfig(server.URL), option.WithHTTPClient(server.Client()), option.WithRequestTimeout(time.Second),
	)
	require.NoError(t, err)

	_, err = provider.Start(t.Context(), testStartRequest())
	require.ErrorIs(t, err, lib.ErrProviderStartAmbiguous)
	require.Equal(t, int32(1), requests.Load())
}

// A failed dial proves nothing reached the provider, so the start is safe to retry rather than
// settled as an unknown outcome.
func TestOpenAITransportFailure(t *testing.T) {
	t.Parallel()

	// A port nothing listens on.
	provider, err := lib.NewOpenAI(testConfig("https://127.0.0.1:1"), option.WithMaxRetries(0))
	require.NoError(t, err)

	_, err = provider.Start(t.Context(), testStartRequest())
	require.ErrorIs(t, err, lib.ErrProviderRetryable)
}

// A start whose deadline passes before the request leaves, here during a TLS handshake that never
// completes, cannot have been accepted: it is retried rather than settled as an unknown outcome.
func TestOpenAIStartStalledBeforeSending(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var (
		mu    sync.Mutex
		conns []net.Conn
	)

	t.Cleanup(func() {
		_ = listener.Close()

		mu.Lock()
		defer mu.Unlock()

		for _, conn := range conns {
			_ = conn.Close()
		}
	})

	// Accepts connections and never speaks, so the handshake stalls.
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			mu.Lock()

			conns = append(conns, conn)

			mu.Unlock()
		}
	}()

	provider, err := lib.NewOpenAI(
		testConfig("https://"+listener.Addr().String()), option.WithRequestTimeout(500*time.Millisecond),
	)
	require.NoError(t, err)

	_, err = provider.Start(t.Context(), testStartRequest())
	require.ErrorIs(t, err, lib.ErrProviderRetryable)
}

// An unknown status is polled again rather than settled: settling on a state this service does not
// understand would throw away work already paid for.
func TestOpenAIUnknownStatus(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t, &scriptedProvider{status: http.StatusOK, body: response("thinking", "")})

	call, err := provider.Start(t.Context(), testStartRequest())
	require.NoError(t, err)
	require.Equal(t, lib.ProviderCallRunning, call.State)
	require.False(t, call.State.Terminal())
}
