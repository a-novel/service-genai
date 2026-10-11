package connectors_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// scriptedProvider stands in for the Responses API. It records the last request so the composition
// assertions can read it, and answers with whatever the case scripted.
type scriptedProvider struct {
	status int
	body   string
	// cancel, when set, answers cancel requests instead.
	cancel *scriptedProvider
	// events, when set, answers as a server-sent event stream instead of JSON.
	events bool

	lastPath  string
	lastQuery string
	lastBody  json.RawMessage
}

func (script *scriptedProvider) serve(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		script.lastPath = request.URL.Path
		script.lastQuery = request.URL.RawQuery

		if request.Body != nil {
			script.lastBody, _ = io.ReadAll(request.Body)
		}

		answer := script
		if script.cancel != nil && strings.HasSuffix(request.URL.Path, "/cancel") {
			answer = script.cancel
		}

		writer.Header().Set("Content-Type", lo.Ternary(answer.events, "text/event-stream", "application/json"))
		writer.WriteHeader(answer.status)
		_, _ = writer.Write([]byte(answer.body))
	}))

	t.Cleanup(server.Close)

	return server
}

// testConfig points the connector at the given endpoint.
func testConfig(baseURL string) connectors.OpenAIConfig {
	return connectors.OpenAIConfig{BaseURL: baseURL, APIKey: "test-key"}
}

func newTestProvider(t *testing.T, script *scriptedProvider) *connectors.OpenAI {
	t.Helper()

	server := script.serve(t)

	// MaxRetries(0) so a scripted 5xx surfaces as one classified error instead of the SDK's own
	// retry loop deciding first.
	provider, err := connectors.NewOpenAI(
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

func testStartRequest() *connectors.AsyncStartRequest {
	return &connectors.AsyncStartRequest{Payload: json.RawMessage(testPayload)}
}

// response builds a Responses API body around the given status and extra fields.
func response(status string, extra string) string {
	body := `{"id": "resp_1", "status": "` + status + `", "model": "gpt-5.6-terra-2026-01-01",` +
		` "reasoning": {"effort": "medium"}, "created_at": 1760000000`
	if extra != "" {
		body += ", " + extra
	}

	return body + "}"
}

// sse writes events as the provider streams them.
func sse(events ...string) string {
	var body strings.Builder

	for _, event := range events {
		body.WriteString("data: " + event + "\n\n")
	}

	return body.String()
}

// lifecycleEvent is a stream event carrying the response in the given status.
func lifecycleEvent(kind string, sequence int, status string) string {
	return `{"type": "response.` + kind + `", "sequence_number": ` + strconv.Itoa(sequence) +
		`, "response": ` + response(status, "") + `}`
}

// deltaEvent is a stream event carrying output text.
func deltaEvent(sequence int) string {
	return `{"type": "response.output_text.delta", "sequence_number": ` + strconv.Itoa(sequence) + `, "delta": "the"}`
}

const (
	testRefusal = `"output": [{"type": "message", "content": [{"type": "refusal", "refusal": "no"}]}]`
	testUsage   = `"service_tier": "flex", "completed_at": 1760000090.5, "usage": {
		"input_tokens": 1000, "input_tokens_details": {"cached_tokens": 200, "cache_write_tokens": 300},
		"output_tokens": 500, "output_tokens_details": {"reasoning_tokens": 100}, "total_tokens": 1500
	}`
)

func TestOpenAI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		script *scriptedProvider

		expectState connectors.CallState
		expectUsage *connectors.Usage
		expectTier  string
		// expectModel and expectClass default to the scripted response's own.
		expectModel     string
		expectClass     string
		expectCompleted bool
		expectReason    string
		expectRetryable bool
		expectErr       error
		// expectRejection marks a definitive start rejection; expectBody is the error body it carries.
		expectRejection bool
		expectBody      string
	}{
		{
			// Polled again rather than settled: settling on a state this service does not understand
			// would throw away work already paid for.
			name: "Success/UnknownStatus",

			script: &scriptedProvider{status: http.StatusOK, body: response("thinking", "")},

			expectState: connectors.CallRunning,
		},
		{
			name: "Success/Completed",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", "")},

			expectState: connectors.CallSucceeded,
		},
		{
			// What actually ran and what it consumed, read off the response for metering.
			name: "Success/CompletedWithUsage",

			script: &scriptedProvider{status: http.StatusOK, body: response("completed", testUsage)},

			expectState: connectors.CallSucceeded,
			expectUsage: &connectors.Usage{
				InputTokens: 1000, CachedInputTokens: 200, CacheWriteTokens: 300, OutputTokens: 500, ReasoningTokens: 100,
			},
			expectTier:      "flex",
			expectCompleted: true,
		},
		{
			// A model that does not reason reports no effort: the call ran at none.
			name: "Success/NoReasoning",

			script: &scriptedProvider{status: http.StatusOK, body: `{"id": "resp_1", "status": "completed", ` +
				`"model": "gpt-5.6-luna-2026-01-01", "created_at": 1760000000}`},

			expectState: connectors.CallSucceeded,
			expectModel: "gpt-5.6-luna-2026-01-01",
			expectClass: "none",
		},
		{
			// A cancelled call is not a free one.
			name: "Success/CancelledWithUsage",

			script: &scriptedProvider{status: http.StatusOK, body: response("cancelled", testUsage)},

			expectState: connectors.CallCancelled,
			expectUsage: &connectors.Usage{
				InputTokens: 1000, CachedInputTokens: 200, CacheWriteTokens: 300, OutputTokens: 500, ReasoningTokens: 100,
			},
			expectTier:      "flex",
			expectCompleted: true,
		},
		{
			// Background mode answers before the model has run: the identifier is recorded now and
			// polled later.
			name: "Success/QueuedIsNotTerminal",

			script: &scriptedProvider{status: http.StatusOK, body: response("queued", "")},

			expectState: connectors.CallQueued,
		},
		{
			// The output stopped at the caller's own max_output_tokens. Finished, and the caller's to read.
			name: "Success/Incomplete",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"incomplete", `"incomplete_details": {"reason": "max_output_tokens"}`,
			)},

			expectState: connectors.CallSucceeded,
		},
		{
			name: "Success/RetryableFailure",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"failed", `"error": {"code": "server_error", "message": "the model failed"}`,
			)},

			expectState:     connectors.CallFailed,
			expectReason:    "the model failed",
			expectRetryable: true,
		},
		{
			name: "Success/PolicyFailure",

			script: &scriptedProvider{status: http.StatusOK, body: response(
				"failed", `"error": {"code": "invalid_prompt", "message": "the prompt was rejected"}`,
			)},

			expectState:  connectors.CallFailed,
			expectReason: "the prompt was rejected",
		},
		{
			// Nothing was accepted, so another start costs nothing.
			name: "Error/RateLimited",

			script: &scriptedProvider{status: http.StatusTooManyRequests, body: `{"error":{"message":"slow down"}}`},

			expectErr: connectors.ErrRetryable,
		},
		{
			name: "Error/ProviderFaultIsAmbiguous",

			script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

			expectErr: connectors.ErrStartAmbiguous,
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
				var rejection *connectors.RejectionError

				require.ErrorAs(t, err, &rejection)
				require.NotErrorIs(t, err, connectors.ErrRetryable)

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

			require.Equal(t, testCase.expectUsage, call.Usage)
			require.Equal(t, testCase.expectTier, call.Tier)

			if !call.State.Terminal() {
				require.Empty(t, call.Response)
				require.Empty(t, call.Model)
				require.Empty(t, call.Class)

				return
			}

			// The provider's answer, as it sent it, and what actually ran, by the provider's clock.
			require.Equal(t, testCase.script.body, string(call.Response))
			require.Equal(t, lo.CoalesceOrEmpty(testCase.expectModel, "gpt-5.6-terra-2026-01-01"), call.Model)
			require.Equal(t, lo.CoalesceOrEmpty(testCase.expectClass, "medium"), call.Class)
			require.Equal(t, time.Unix(1760000000, 0).UTC(), call.StartedAt)

			if testCase.expectCompleted {
				require.Equal(t, time.Unix(1760000090, 500_000_000).UTC(), *call.CompletedAt)
			} else {
				require.Nil(t, call.CompletedAt)
			}
		})
	}

	// The connector writes the fields the Async mode owns and forwards everything else as the caller wrote
	// it.
	t.Run("Request", func(t *testing.T) {
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

				_, err := provider.Start(t.Context(), &connectors.AsyncStartRequest{Payload: json.RawMessage(testCase.payload)})
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
	})

	t.Run("Get", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name string

			script *scriptedProvider

			expectState connectors.CallState
			expectErr   error
		}{
			{
				name: "Success",

				script: &scriptedProvider{status: http.StatusOK, body: response("completed", "")},

				expectState: connectors.CallSucceeded,
			},
			{
				// Gone past the provider's retention window: not something another read fixes.
				name: "Error/NotFound",

				script: &scriptedProvider{status: http.StatusNotFound, body: `{"error":{"message":"no such response"}}`},
			},
			{
				name: "Error/Retryable",

				script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

				expectErr: connectors.ErrRetryable,
			},
			{
				// Refused credentials are this service's configuration, not the operation's fate: once the
				// key is fixed, the result is read again.
				name: "Error/Unauthorized",

				script: &scriptedProvider{status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`},

				expectErr: connectors.ErrRetryable,
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
						require.NotErrorIs(t, err, connectors.ErrRetryable)
					}

					return
				}

				require.NoError(t, err)
				require.Equal(t, testCase.expectState, call.State)
				require.Equal(t, testCase.script.body, string(call.Response))
				require.Equal(t, "/responses/resp_1", testCase.script.lastPath)
			})
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name string

			script *scriptedProvider

			expectState connectors.CallState
			// expectPath is the last path requested, the cancel itself unless stated.
			expectPath string
			expectErr  error
		}{
			{
				name: "Success",

				script: &scriptedProvider{status: http.StatusOK, body: response("cancelled", "")},

				expectState: connectors.CallCancelled,
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

				expectState: connectors.CallSucceeded,
				expectPath:  "/responses/resp_1",
			},
			{
				name: "Error/Retryable",

				script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

				expectErr: connectors.ErrRetryable,
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
	})

	// A start the provider may have received but never answered cannot be retried as a new one.
	t.Run("StartTimeoutIsAmbiguous", func(t *testing.T) {
		t.Parallel()

		var requests atomic.Int32

		release := make(chan struct{})
		defer close(release)

		server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			requests.Add(1)

			<-release
		}))
		t.Cleanup(server.Close)

		provider, err := connectors.NewOpenAI(
			testConfig(server.URL), option.WithHTTPClient(server.Client()), option.WithRequestTimeout(time.Second),
		)
		require.NoError(t, err)

		_, err = provider.Start(t.Context(), testStartRequest())
		require.ErrorIs(t, err, connectors.ErrStartAmbiguous)
		require.Equal(t, int32(1), requests.Load())
	})

	// A failed dial proves nothing reached the provider, so the start is safe to retry rather than
	// settled as an unknown outcome.
	t.Run("TransportFailure", func(t *testing.T) {
		t.Parallel()

		// A port nothing listens on.
		provider, err := connectors.NewOpenAI(testConfig("https://127.0.0.1:1"), option.WithMaxRetries(0))
		require.NoError(t, err)

		_, err = provider.Start(t.Context(), testStartRequest())
		require.ErrorIs(t, err, connectors.ErrRetryable)
	})

	// A start whose deadline passes before the request leaves, here during a TLS handshake that never
	// completes, cannot have been accepted: it is retried rather than settled as an unknown outcome.
	t.Run("StartStalledBeforeSending", func(t *testing.T) {
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

		provider, err := connectors.NewOpenAI(
			testConfig("https://"+listener.Addr().String()), option.WithRequestTimeout(500*time.Millisecond),
		)
		require.NoError(t, err)

		_, err = provider.Start(t.Context(), testStartRequest())
		require.ErrorIs(t, err, connectors.ErrRetryable)
	})

	// A streamed start holds the provider's creation stream until the provider begins the call:
	// leaving it while still queued can leave the call queued for good.
	t.Run("StreamedStart", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name string

			events []string

			expectState connectors.CallState
			expectErr   error
		}{
			{
				name: "Success/HeldUntilInProgress",

				events: []string{
					lifecycleEvent("created", 0, "queued"),
					lifecycleEvent("queued", 1, "queued"),
					lifecycleEvent("in_progress", 2, "in_progress"),
					deltaEvent(3),
				},

				expectState: connectors.CallRunning,
			},
			{
				// The provider never began it within the hold: the call is recorded queued, for the
				// checks to follow.
				name: "Success/LeftQueued",

				events: []string{lifecycleEvent("created", 0, "queued"), lifecycleEvent("queued", 1, "queued")},

				expectState: connectors.CallQueued,
			},
			{
				// The request left, so the provider may hold a call nothing names.
				name: "Error/EndedBeforeID",

				expectErr: connectors.ErrStartAmbiguous,
			},
		}

		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				t.Parallel()

				script := &scriptedProvider{status: http.StatusOK, events: true, body: sse(testCase.events...)}
				provider := newTestProvider(t, script)

				call, err := provider.Start(t.Context(), &connectors.AsyncStartRequest{
					Payload: json.RawMessage(testPayload), Stream: true,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				var sent map[string]json.RawMessage

				require.NoError(t, json.Unmarshal(script.lastBody, &sent))
				require.JSONEq(t, "true", string(sent["stream"]))
				require.JSONEq(t, "true", string(sent["background"]))

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, "resp_1", call.ID)
				require.Equal(t, testCase.expectState, call.State)
			})
		}
	})

	t.Run("Stream", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name string

			script        *scriptedProvider
			startingAfter *int64

			expectQuery     string
			expectSequences []int64
			expectErr       error
		}{
			{
				// The stream stops at the terminal event, whatever the provider sends after.
				name: "Success",

				script: &scriptedProvider{status: http.StatusOK, events: true, body: sse(
					deltaEvent(3), lifecycleEvent("completed", 4, "completed"), deltaEvent(5),
				)},
				startingAfter: new(int64(2)),

				expectQuery:     "starting_after=2&stream=true",
				expectSequences: []int64{3, 4},
			},
			{
				name: "Success/FromTheStart",

				script: &scriptedProvider{status: http.StatusOK, events: true, body: sse(deltaEvent(0))},

				expectQuery:     "stream=true",
				expectSequences: []int64{0},
			},
			{
				// Past the stream window, or a call not created to be streamed.
				name: "Error/Unavailable",

				script: &scriptedProvider{status: http.StatusBadRequest, body: `{"error": {"message": "too old"}}`},

				expectQuery: "stream=true",
				expectErr:   connectors.ErrStreamUnavailable,
			},
			{
				name: "Error/ProviderFailure",

				script: &scriptedProvider{status: http.StatusInternalServerError, body: `{}`},

				expectQuery: "stream=true",
				expectErr:   connectors.ErrRetryable,
			},
		}

		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				t.Parallel()

				provider := newTestProvider(t, testCase.script)

				var (
					sequences []int64
					err       error
				)

				for event, eventErr := range provider.Stream(t.Context(), &connectors.StreamRequest{
					ID: "resp_1", StartingAfter: testCase.startingAfter,
				}) {
					if eventErr != nil {
						err = eventErr

						break
					}

					sequences = append(sequences, event.Sequence)
				}

				require.ErrorIs(t, err, testCase.expectErr)
				require.Equal(t, testCase.expectSequences, sequences)
				require.Equal(t, "/responses/resp_1", testCase.script.lastPath)
				require.Equal(t, testCase.expectQuery, testCase.script.lastQuery)
			})
		}
	})
}

func TestNewOpenAI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		config connectors.OpenAIConfig

		expectErr error
	}{
		{
			name: "Success",

			config: testConfig("https://provider.test/v1"),
		},
		{
			name: "Error/NoBaseURL",

			config: testConfig(""),

			expectErr: connectors.ErrNotConfigured,
		},
		{
			name: "Error/NoAPIKey",

			config: connectors.OpenAIConfig{BaseURL: "https://provider.test/v1"},

			expectErr: connectors.ErrNotConfigured,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := connectors.NewOpenAI(testCase.config)
			require.ErrorIs(t, err, testCase.expectErr)
		})
	}
}
