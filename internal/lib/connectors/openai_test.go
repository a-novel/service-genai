package connectors_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
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

func (script *scriptedProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
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
}

func (script *scriptedProvider) serve(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewTLSServer(script)
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

// unauthorizedBody is how OpenAI refuses a key: its message echoes the key's ends.
const unauthorizedBody = `{"error":{"message":"Incorrect API key provided: sk-proj-****abcd.",` +
	`"type":"invalid_request_error","code":"invalid_api_key"}}`

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
		// expectHidden is text of the provider's answer the error must leave out.
		expectHidden string
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
			// A lock the provider releases: nothing ran, so another start costs nothing.
			name: "Error/Conflict",

			script: &scriptedProvider{status: http.StatusConflict, body: `{"error":{"message":"locked"}}`},

			expectErr: connectors.ErrRetryable,
		},
		{
			name: "Error/ProviderFaultIsAmbiguous",

			script: &scriptedProvider{status: http.StatusBadGateway, body: `{"error":{"message":"bad gateway"}}`},

			expectErr: connectors.ErrStartAmbiguous,
		},
		{
			// Accepted, but no id can be read off the answer: the provider may run a call nothing names.
			name: "Error/UnreadableIsAmbiguous",

			script: &scriptedProvider{status: http.StatusOK, body: `{"id": "resp_1", "created_at": "soon"}`},

			expectErr: connectors.ErrStartAmbiguous,
		},
		{
			// A compatible endpoint that answers without naming the call leaves nothing to follow either.
			name: "Error/UnnamedIsAmbiguous",

			script: &scriptedProvider{status: http.StatusOK, body: `{"id": "", "status": "queued"}`},

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
			// The service's own key, not the caller's request: retried, and its body never passed on, nor
			// logged: it echoes part of the key.
			name: "Error/Unauthorized",

			script: &scriptedProvider{status: http.StatusUnauthorized, body: unauthorizedBody},

			expectErr:    connectors.ErrRetryable,
			expectHidden: "sk-proj-****abcd",
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

			if testCase.expectHidden != "" {
				require.NotContains(t, err.Error(), testCase.expectHidden)
			}

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

				expectSent := map[string]any{
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
				}
				require.Equal(t, expectSent, sent)

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

			expectState  connectors.CallState
			expectErr    error
			expectHidden string
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

				script: &scriptedProvider{status: http.StatusUnauthorized, body: unauthorizedBody},

				expectErr:    connectors.ErrRetryable,
				expectHidden: "sk-proj-****abcd",
			},
			{
				// A garbled answer says nothing about the operation, which the next read may find intact.
				name: "Error/Unreadable",

				script: &scriptedProvider{status: http.StatusOK, body: `{"id": "resp_1", "status": `},

				expectErr: connectors.ErrRetryable,
			},
			{
				// No read gets past the size the connector reads, so asking again cannot help.
				name: "Error/TooLarge",

				script: &scriptedProvider{
					status: http.StatusOK,
					body:   response("completed", `"pad": "`+strings.Repeat("x", connectors.ResponseSizeCeiling)+`"`),
				},

				expectErr: connectors.ErrResponseTooLarge,
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
					}

					if !errors.Is(testCase.expectErr, connectors.ErrRetryable) {
						require.NotErrorIs(t, err, connectors.ErrRetryable)
					}

					if testCase.expectHidden != "" {
						require.NotContains(t, err.Error(), testCase.expectHidden)
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
			{
				name: "Error/Unreadable",

				script: &scriptedProvider{status: http.StatusOK, body: `{"id": "resp_1", "status": `},

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

		provider, err := connectors.NewOpenAI(
			testConfig("https://"+listenSilently(t)), option.WithRequestTimeout(500*time.Millisecond),
		)
		require.NoError(t, err)

		_, err = provider.Start(t.Context(), testStartRequest())
		require.ErrorIs(t, err, connectors.ErrRetryable)
	})

	// A streamed start holds the provider's creation stream until the provider begins the call:
	// leaving it while still queued can leave the call queued for good.
	t.Run("StreamedStart", func(t *testing.T) {
		t.Parallel()

		errRecord := errors.New("record failed")

		testCases := []struct {
			name string

			events      []string
			acceptedErr error

			expectState connectors.CallState
			// expectAccepted is how the call stood each time it went to the hook: once, when named.
			expectAccepted []connectors.CallState
			expectErr      error
		}{
			{
				// The call is named queued, and recorded then, before the hold ends.
				name: "Success/HeldUntilInProgress",

				events: []string{
					lifecycleEvent("created", 0, "queued"),
					lifecycleEvent("queued", 1, "queued"),
					lifecycleEvent("in_progress", 2, "in_progress"),
					deltaEvent(3),
				},

				expectState:    connectors.CallRunning,
				expectAccepted: []connectors.CallState{connectors.CallQueued},
			},
			{
				// The provider never began it within the hold: the call is recorded queued, for the
				// checks to follow.
				name: "Success/LeftQueued",

				events: []string{lifecycleEvent("created", 0, "queued"), lifecycleEvent("queued", 1, "queued")},

				expectState:    connectors.CallQueued,
				expectAccepted: []connectors.CallState{connectors.CallQueued},
			},
			{
				// A call that could not be recorded ends the start with the reason.
				name: "Error/AcceptedFailed",

				events: []string{
					lifecycleEvent("created", 0, "queued"),
					lifecycleEvent("in_progress", 1, "in_progress"),
				},
				acceptedErr: errRecord,

				expectAccepted: []connectors.CallState{connectors.CallQueued},
				expectErr:      errRecord,
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

				var accepted []connectors.CallState

				call, err := provider.Start(t.Context(), &connectors.AsyncStartRequest{
					Payload: json.RawMessage(testPayload), Stream: true,
					Accepted: func(call *connectors.Call) error {
						accepted = append(accepted, call.State)

						return testCase.acceptedErr
					},
				})
				require.ErrorIs(t, err, testCase.expectErr)
				require.Equal(t, testCase.expectAccepted, accepted)

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
			{
				// Every stream resumed before an event too large to read would end on it: poll instead.
				name: "Error/EventTooLarge",

				script: &scriptedProvider{status: http.StatusOK, events: true, body: sse(
					deltaEvent(3), `{"type":"response.output_text.delta","delta":"`+strings.Repeat("a", 33<<20)+`"}`,
				)},

				expectQuery:     "stream=true",
				expectSequences: []int64{3},
				expectErr:       connectors.ErrStreamUnavailable,
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

	// Notifications are verified with the endpoint's signing secret, as the Standard Webhooks
	// specification signs them, before they name any call.
	t.Run("Notification", func(t *testing.T) {
		t.Parallel()

		key := []byte("test-signing-key-of-32-bytes-long")
		secret := "whsec_" + base64.StdEncoding.EncodeToString(key)

		testCases := []struct {
			name string

			secret string
			body   string
			header map[string][]string

			expect    *connectors.Notification
			expectErr error
		}{
			{
				name: "Success",

				secret: secret,
				body:   webhookEvent("response.completed"),
				header: signWebhook(key, webhookEvent("response.completed")),

				expect: &connectors.Notification{CallID: "resp_1"},
			},
			{
				// The endpoint may be subscribed to more than responses.
				name: "Error/Ignored",

				secret: secret,
				body:   webhookEvent("batch.completed"),
				header: signWebhook(key, webhookEvent("batch.completed")),

				expectErr: connectors.ErrNotificationIgnored,
			},
			{
				name: "Error/BadSignature",

				secret: secret,
				body:   webhookEvent("response.completed"),
				header: signWebhook(key, webhookEvent("response.failed")),

				expectErr: connectors.ErrNotificationInvalid,
			},
			{
				// Without the signing secret, nothing can be verified, so nothing is read.
				name: "Error/NoSecret",

				body:   webhookEvent("response.completed"),
				header: signWebhook(key, webhookEvent("response.completed")),

				expectErr: connectors.ErrModeUnsupported,
			},
		}

		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				t.Parallel()

				provider, err := connectors.NewOpenAI(connectors.OpenAIConfig{
					BaseURL: "https://provider.test/v1", APIKey: "test-key", WebhookSecret: testCase.secret,
				})
				require.NoError(t, err)

				notification, err := provider.Notification(t.Context(), &connectors.NotificationRequest{
					Body: []byte(testCase.body), Header: testCase.header,
				})
				require.ErrorIs(t, err, testCase.expectErr)
				require.Equal(t, testCase.expect, notification)
			})
		}
	})

	// Conformance checks the connectors the server builds against the Async mode's requirements in
	// docs/providers.md. It calls the provider and costs money, so it runs only when PROVIDER_CONFORMANCE
	// is true. Run it before changing a connector or the account it reaches.
	t.Run("Conformance", func(t *testing.T) {
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

		t.Run("Success", func(t *testing.T) {
			t.Parallel()

			started, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{
				Payload: conformancePayload("gpt-5.6-luna", "low"),
			})
			require.NoError(t, err)
			// Background mode, with the caller's store: false: the id comes back before the model runs.
			require.NotEmpty(t, started.ID)
			require.False(t, started.State.Terminal())

			finished := awaitCall(t, connector, started.ID)
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
			require.JSONEq(t, responseOutput(t, finished.Response), responseOutput(t, again.Response))
		})

		// Streaming mode: the start waits for the provider to begin the call, then a stream reads its
		// events, as sent, through the terminal one.
		t.Run("Success/Streamed", func(t *testing.T) {
			t.Parallel()

			streaming, err := registry.Streaming("openai")
			require.NoError(t, err)

			var named *connectors.Call

			started, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{
				Payload: conformancePayload("gpt-5.6-luna", "low"), Stream: true,
				Accepted: func(call *connectors.Call) error {
					named = call

					return nil
				},
			})
			require.NoError(t, err)
			require.NotEmpty(t, started.ID)
			// Left only once the provider began it: a call left queued can stay queued for good.
			require.NotEqual(t, connectors.CallQueued, started.State)
			// Named while still queued, so a check records it before the hold ends.
			require.NotNil(t, named)
			require.Equal(t, started.ID, named.ID)
			require.Equal(t, connectors.CallQueued, named.State)

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
			started, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{
				Payload: conformancePayload("gpt-5.6-luna", "low"),
			})
			require.NoError(t, err)

			finished := awaitCall(t, connector, started.ID)
			require.Equal(t, connectors.CallSucceeded, finished.State, finished.Reason)

			cancelled, err := connector.Cancel(t.Context(), started.ID)
			require.NoError(t, err)
			require.Equal(t, connectors.CallSucceeded, cancelled.State)
			require.JSONEq(t, responseOutput(t, finished.Response), responseOutput(t, cancelled.Response))
		})

		t.Run("Success/Cancel", func(t *testing.T) {
			t.Parallel()

			// A slow model and effort, so the cancel lands while the call still runs.
			started, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{
				Payload: conformancePayload("gpt-5.6-sol", "high"),
			})
			require.NoError(t, err)

			_, err = connector.Cancel(t.Context(), started.ID)
			require.NoError(t, err)
			require.Equal(t, connectors.CallCancelled, awaitCall(t, connector, started.ID).State)
		})

		t.Run("Error/Rejected", func(t *testing.T) {
			t.Parallel()

			// A request the provider refuses fails before anything runs, with the provider's reason.
			_, err := connector.Start(t.Context(), &connectors.AsyncStartRequest{
				Payload: conformancePayload("no-such-model", "low"),
			})

			var rejection *connectors.RejectionError

			require.ErrorAs(t, err, &rejection)
			require.True(t, json.Valid(rejection.Response))
		})
	})

	t.Run("New", func(t *testing.T) {
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
	})
}

// listenSilently accepts connections and never speaks on them, so a TLS handshake stalls. It returns
// the address it listens on.
func listenSilently(t *testing.T) string {
	t.Helper()

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

	return listener.Addr().String()
}

// webhookEvent is an OpenAI webhook event of the given type about the response resp_1.
func webhookEvent(kind string) string {
	return `{"id": "evt_1", "object": "event", "created_at": 1760000000, "type": "` + kind +
		`", "data": {"id": "resp_1"}}`
}

// signWebhook returns the headers OpenAI sends with a body, signed with the endpoint's key.
func signWebhook(key []byte, body string) map[string][]string {
	id, timestamp := "evt_1", strconv.FormatInt(time.Now().Unix(), 10)

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "." + body))

	return map[string][]string{
		"Webhook-Id":        {id},
		"Webhook-Timestamp": {timestamp},
		"Webhook-Signature": {"v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))},
	}
}

// conformancePayload is what a caller sends: its own model, effort, schema and privacy fields. The
// schema exercises the strict subset callers rely on: nesting, an array, an enum and a nullable field.
func conformancePayload(model, effort string) json.RawMessage {
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

// awaitCall polls a call until it reaches a terminal state.
func awaitCall(t *testing.T, connector connectors.Async, id string) *connectors.Call {
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

// responseOutput reads the part of a response that must survive a re-read unchanged.
func responseOutput(t *testing.T, response json.RawMessage) string {
	t.Helper()

	var body struct {
		Output json.RawMessage `json:"output"`
	}

	require.NoError(t, json.Unmarshal(response, &body))
	require.NotEmpty(t, body.Output)

	return string(body.Output)
}
