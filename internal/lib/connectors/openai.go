package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
)

// responsesPath is the endpoint requests are posted to.
const responsesPath = "responses"

// readTimeout bounds one read or cancel, so a hung call cannot hold a sweep.
const readTimeout = 10 * time.Second

// healthCheckTimeout is how long a connection may stay silent before a ping checks it, and how long
// the ping may go unanswered.
const healthCheckTimeout = 5 * time.Second

// readOptions make a read a single bounded attempt: the next check is the retry.
var readOptions = []option.RequestOption{option.WithMaxRetries(0), option.WithRequestTimeout(readTimeout)}

// ErrNotConfigured is returned when the connector has no endpoint or no credentials.
var ErrNotConfigured = errors.New("provider base URL and API key are required")

// OpenAIConfig is how the connector reaches the Responses API. Any endpoint serving it fits.
type OpenAIConfig struct {
	BaseURL string
	APIKey  string
	// WebhookSecret is the signing secret of the webhook endpoint OpenAI notifies. Empty, the
	// connector does not read notifications.
	WebhookSecret string
}

// OpenAI runs calls on the Responses API. It offers the Async and Streaming modes, and reads
// OpenAI's webhook notifications once given their signing secret.
type OpenAI struct {
	client        openai.Client
	webhookSecret string
}

// NewOpenAI refuses an incomplete configuration, so a bad revision fails at startup instead of on
// its first call. Options are forwarded to the SDK, so a test can swap the HTTP client.
//
// The SDK still reads OPENAI_ORG_ID, OPENAI_PROJECT_ID and OPENAI_CUSTOM_HEADERS from the
// environment.
func NewOpenAI(config OpenAIConfig, opts ...option.RequestOption) (*OpenAI, error) {
	if config.BaseURL == "" || config.APIKey == "" {
		return nil, ErrNotConfigured
	}

	opts = append([]option.RequestOption{
		option.WithBaseURL(config.BaseURL), option.WithAPIKey(config.APIKey), option.WithHTTPClient(healthCheckedClient()),
	}, opts...)

	return &OpenAI{client: openai.NewClient(opts...), webhookSecret: config.WebhookSecret}, nil
}

// healthCheckedClient pings a connection the provider has gone quiet on, and drops it when the ping
// goes unanswered. Go otherwise keeps sending calls into a dead connection, and each start sent there
// times out as ambiguous.
func healthCheckedClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultClient
	}

	transport = transport.Clone()
	transport.HTTP2 = &http.HTTP2Config{SendPingTimeout: healthCheckTimeout, PingTimeout: healthCheckTimeout}

	return &http.Client{Transport: transport}
}

// Start posts the caller's payload in the Async mode.
//
// background makes crash-safety possible: the call returns an identifier immediately, so a restarted
// check resumes an operation already paid for. stream is off, because the result is read back by
// polling.
func (provider *OpenAI) Start(ctx context.Context, request *AsyncStartRequest) (*Call, error) {
	ctx, span := otel.Tracer().Start(ctx, "connectors.OpenAI.Start")
	defer span.End()

	body, err := asyncEnvelope(request.Payload, request.Stream)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("write async envelope: %w", err))
	}

	// Whether the request fully left decides whether a failed start may have been accepted. The SDK
	// returns a bare deadline error even when the deadline hit during the dial, so the error alone
	// cannot tell.
	var sent atomic.Bool

	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) { sent.Store(info.Err == nil) },
	})

	if request.Stream {
		call, err := provider.holdStream(ctx, body)
		if err != nil {
			return nil, otel.ReportError(span, classifyOpenAIStartError(err, sent.Load()))
		}

		return call, nil
	}

	response := &responses.Response{}

	err = provider.client.Post(ctx, responsesPath, body, response, option.WithMaxRetries(0))
	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIStartError(err, sent.Load()))
	}

	return providerCallOf(response), nil
}

// Stream reads the operation's events after the cursor. openai-go's GetStreaming sends a body on
// the GET, which the API refuses (openai-go#660), so the request is built here.
func (provider *OpenAI) Stream(ctx context.Context, request *StreamRequest) iter.Seq2[*StreamEvent, error] {
	return func(yield func(*StreamEvent, error) bool) {
		ctx, span := otel.Tracer().Start(ctx, "connectors.OpenAI.Stream")
		defer span.End()

		opts := []option.RequestOption{option.WithMaxRetries(0), option.WithQuery("stream", "true")}
		if request.StartingAfter != nil {
			opts = append(opts, option.WithQuery("starting_after", strconv.FormatInt(*request.StartingAfter, 10)))
		}

		var raw *http.Response

		err := provider.client.Get(ctx, responsesPath+"/"+request.ID, nil, &raw, opts...)
		if err != nil {
			yield(nil, otel.ReportError(span, classifyOpenAIStreamError(err)))

			return
		}

		decoder := ssestream.NewDecoder(raw)
		defer func() { _ = decoder.Close() }()

		for decoder.Next() {
			data := decoder.Event().Data

			var head responses.ResponseStreamEventUnion

			err = json.Unmarshal(data, &head)
			if err != nil {
				yield(nil, otel.ReportError(span, fmt.Errorf("decode event: %w", err)))

				return
			}

			event := &StreamEvent{Sequence: head.SequenceNumber, Data: data, Terminal: terminalEvents[head.Type]}
			if !yield(event, nil) || event.Terminal {
				return
			}
		}

		// A stream cut before its terminal event resumes after the last event the caller received.
		err = decoder.Err()
		if err != nil {
			yield(nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrRetryable, err)))
		}
	}
}

// endedEvents are the webhook events that report a response ended, whichever way.
var endedEvents = map[string]bool{
	"response.completed":  true,
	"response.failed":     true,
	"response.cancelled":  true,
	"response.incomplete": true,
}

// terminalEvents end an operation's stream. A cancelled operation has none: its stream just ends.
var terminalEvents = map[string]bool{
	"response.completed":  true,
	"response.failed":     true,
	"response.incomplete": true,
	"error":               true,
}

// classifyOpenAIStreamError tells a stream the provider refuses from one worth opening again.
func classifyOpenAIStreamError(err error) error {
	var apiErr *openai.Error

	if !errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %w", ErrRetryable, err)
	}

	err = withProviderMessage(err, apiErr)

	switch status := apiErr.StatusCode; {
	case status == http.StatusBadRequest, status == http.StatusNotFound:
		// Past the stream window, or never created to be streamed.
		return fmt.Errorf("%w: %w", ErrStreamUnavailable, err)
	case status == http.StatusTooManyRequests, status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %w", ErrRetryable, err)
	}

	return err
}

// Get reads an operation, and is also the re-attach path.
func (provider *OpenAI) Get(ctx context.Context, id string) (*Call, error) {
	ctx, span := otel.Tracer().Start(ctx, "connectors.OpenAI.Get")
	defer span.End()

	span.SetAttributes(attribute.String("provider.call_id", id))

	response, err := provider.client.Responses.Get(ctx, id, responses.ResponseGetParams{}, readOptions...)
	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIError(err))
	}

	return providerCallOf(response), nil
}

// Cancel stops an operation so an abandoned generation stops costing.
func (provider *OpenAI) Cancel(ctx context.Context, id string) (*Call, error) {
	ctx, span := otel.Tracer().Start(ctx, "connectors.OpenAI.Cancel")
	defer span.End()

	span.SetAttributes(attribute.String("provider.call_id", id))

	response, err := provider.client.Responses.Cancel(ctx, id, readOptions...)

	// OpenAI refuses to cancel a response that already finished. Its result is still there, and paid.
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
		call, getErr := provider.Get(ctx, id)
		if getErr != nil {
			return nil, otel.ReportError(span, getErr)
		}

		return call, nil
	}

	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIError(err))
	}

	return providerCallOf(response), nil
}

// Notification verifies one of OpenAI's webhook events with the endpoint's signing secret, and names
// the response it reports ended.
func (provider *OpenAI) Notification(ctx context.Context, request *NotificationRequest) (*Notification, error) {
	_, span := otel.Tracer().Start(ctx, "connectors.OpenAI.Notification")
	defer span.End()

	if provider.webhookSecret == "" {
		return nil, otel.ReportError(span, fmt.Errorf("%w: no webhook signing secret", ErrModeUnsupported))
	}

	event, err := provider.client.Webhooks.Unwrap(
		request.Body, http.Header(request.Header), option.WithWebhookSecret(provider.webhookSecret),
	)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrNotificationInvalid, err))
	}

	span.SetAttributes(attribute.String("notification.type", event.Type))

	if !endedEvents[event.Type] {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %s", ErrNotificationIgnored, event.Type))
	}

	return &Notification{CallID: event.Data.ID}, nil
}

// holdStream creates the operation in streaming mode and reads its stream until the provider begins
// it: leaving while it is still queued can leave it queued for good. A deadline cut short after
// the operation's id came returns the call still queued, for checks to follow.
func (provider *OpenAI) holdStream(ctx context.Context, body json.RawMessage) (*Call, error) {
	ctx, span := otel.Tracer().Start(ctx, "connectors.OpenAI(holdStream)")
	defer span.End()

	var raw *http.Response

	err := provider.client.Post(ctx, responsesPath, body, &raw, option.WithMaxRetries(0))
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	decoder := ssestream.NewDecoder(raw)
	defer func() { _ = decoder.Close() }()

	var call *Call

	for decoder.Next() {
		var event responses.ResponseStreamEventUnion

		if json.Unmarshal(decoder.Event().Data, &event) != nil || event.Response.ID == "" {
			continue
		}

		call = providerCallOf(&event.Response)
		if call.State != CallQueued {
			break
		}
	}

	if call == nil {
		// The request left, so the provider may hold the operation; nothing names it.
		return nil, otel.ReportError(span, fmt.Errorf("%w: the stream ended before the operation's id", ErrStartAmbiguous))
	}

	return call, nil
}

// asyncEnvelope writes the fields the Async mode owns over the caller's payload. The other values
// are forwarded as sent: they are decoded only as far as the top-level object, and re-encoded
// without escaping.
func asyncEnvelope(payload json.RawMessage, stream bool) (json.RawMessage, error) {
	var fields map[string]json.RawMessage

	err := json.Unmarshal(payload, &fields)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	fields["background"] = json.RawMessage("true")
	fields["stream"] = json.RawMessage(strconv.FormatBool(stream))

	return encodeJSON(fields)
}

// encodeJSON encodes without escaping <, > and &, so the provider's bytes and the caller's reach the
// other side as they were.
func encodeJSON(value any) (json.RawMessage, error) {
	var buffer bytes.Buffer

	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	err := encoder.Encode(value)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// providerCallOf reads the operation's state, and once terminal its usage, off a provider response,
// and keeps the response itself.
//
// A completed or incomplete response is finished: an output cut at the caller's max_output_tokens,
// or a refusal, is the caller's to read.
func providerCallOf(response *responses.Response) *Call {
	call := &Call{ID: response.ID, State: CallRunning}

	switch response.Status {
	case responses.ResponseStatusCompleted, responses.ResponseStatusIncomplete:
		call.State = CallSucceeded
	case responses.ResponseStatusFailed:
		call.State = CallFailed
		call.Reason = response.Error.Message
		call.Retryable = retryableFailure(response.Error.Code)
	case responses.ResponseStatusCancelled:
		call.State = CallCancelled
	case responses.ResponseStatusQueued:
		call.State = CallQueued
	case responses.ResponseStatusInProgress:
	default:
		// An unknown status is treated as still running rather than terminal: polling again is
		// cheap, and settling on a status we do not understand throws away work already paid for.
	}

	if !call.State.Terminal() {
		return call
	}

	call.Response = json.RawMessage(response.RawJSON())
	call.Model = response.Model
	call.Class = string(response.Reasoning.Effort)
	call.Tier = string(response.ServiceTier)

	if response.CreatedAt > 0 {
		call.StartedAt = unixTime(response.CreatedAt)
	}

	// A model that does not reason reports no effort.
	if call.Class == "" {
		call.Class = string(shared.ReasoningEffortNone)
	}

	if response.JSON.CompletedAt.Valid() && response.CompletedAt > 0 {
		completedAt := unixTime(response.CompletedAt)
		call.CompletedAt = &completedAt
	}

	// Zero tokens is a real answer and not a missing one, so presence is decided by the provider
	// reporting the field.
	if response.Usage.JSON.TotalTokens.Valid() {
		call.Usage = &Usage{
			InputTokens:       response.Usage.InputTokens,
			CachedInputTokens: response.Usage.InputTokensDetails.CachedTokens,
			CacheWriteTokens:  response.Usage.InputTokensDetails.CacheWriteTokens,
			OutputTokens:      response.Usage.OutputTokens,
			ReasoningTokens:   response.Usage.OutputTokensDetails.ReasoningTokens,
		}
	}

	return call
}

// unixTime reads the provider's fractional Unix seconds.
func unixTime(seconds float64) time.Time {
	whole, fraction := math.Modf(seconds)

	return time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC()
}

// retryableFailure reports whether another attempt at a failed call is worth paying for.
func retryableFailure(code responses.ResponseErrorCode) bool {
	switch code {
	case responses.ResponseErrorCodeServerError,
		responses.ResponseErrorCodeRateLimitExceeded,
		responses.ResponseErrorCodeVectorStoreTimeout:
		return true
	default:
		return false
	}
}

// classifyOpenAIStartError separates definitive rejection from a response that may have been lost.
// sent reports whether the request fully left.
func classifyOpenAIStartError(err error, sent bool) error {
	var apiErr *openai.Error

	if !errors.As(err, &apiErr) {
		// A request that never fully left cannot have been accepted, whatever stopped it: a refused
		// or timed-out dial, a stalled handshake, the start deadline.
		if !sent {
			return fmt.Errorf("%w: %w", ErrRetryable, err)
		}

		return fmt.Errorf("%w: %w", ErrStartAmbiguous, err)
	}

	err = withProviderMessage(err, apiErr)

	switch status := apiErr.StatusCode; {
	case status == http.StatusTooManyRequests && apiErr.Code == "insufficient_quota":
		// Retrying cannot help until an operator adds credit, so the caller gets an answer instead.
		return &RejectionError{Response: errorBody(apiErr), Err: err}
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", ErrRetryable, err)
	case status == http.StatusRequestTimeout, status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %w", ErrStartAmbiguous, err)
	}

	// The request itself, the credentials, the permissions or the model: nothing ran.
	return &RejectionError{Response: errorBody(apiErr), Err: err}
}

// errorBody restores the body the provider sent, which the SDK unwraps to its error object. Empty
// when the provider sent no JSON error.
func errorBody(apiErr *openai.Error) json.RawMessage {
	raw := json.RawMessage(apiErr.RawJSON())
	if !json.Valid(raw) {
		return nil
	}

	body, err := encodeJSON(map[string]json.RawMessage{"error": raw})
	if err != nil {
		return nil
	}

	return body
}

// classifyOpenAIError decides whether reading an operation again is worth it.
//
// Retryable: transport failures, rate limits, provider-side faults, and refused credentials, which
// are this service's configuration and say nothing about the operation. Anything else, a not-found
// above all, means the operation cannot be read.
func classifyOpenAIError(err error) error {
	var apiErr *openai.Error

	if !errors.As(err, &apiErr) {
		// No HTTP response at all: a dial failure, a timeout, a reset connection.
		return fmt.Errorf("%w: %w", ErrRetryable, err)
	}

	err = withProviderMessage(err, apiErr)

	switch status := apiErr.StatusCode; {
	case status == http.StatusTooManyRequests,
		status == http.StatusRequestTimeout,
		status == http.StatusUnauthorized,
		status == http.StatusForbidden,
		status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %w", ErrRetryable, err)
	}

	return fmt.Errorf("provider rejected the request: %w", err)
}

// withProviderMessage adds the provider's explanation, which the SDK leaves out of its error text.
// It reaches server logs; a caller reads the provider's error body instead.
func withProviderMessage(err error, apiErr *openai.Error) error {
	if apiErr.Message == "" {
		return err
	}

	return fmt.Errorf("%w: %s", err, apiErr.Message)
}
