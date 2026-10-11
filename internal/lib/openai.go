package lib

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
)

// responsesPath is the endpoint requests are posted to.
const responsesPath = "responses"

// readTimeout bounds one read or cancel, so a hung call cannot hold a sweep.
const readTimeout = 10 * time.Second

// readOptions make a read a single bounded attempt: the next check is the retry.
var readOptions = []option.RequestOption{option.WithMaxRetries(0), option.WithRequestTimeout(readTimeout)}

// ErrProviderNotConfigured is returned when the adapter has no endpoint or no credentials.
var ErrProviderNotConfigured = errors.New("provider base URL and API key are required")

// OpenAIConfig is how the adapter reaches the Responses API. Any endpoint serving it fits.
type OpenAIConfig struct {
	BaseURL string
	APIKey  string
}

// OpenAI runs calls on the Responses API. It offers the Async mode.
type OpenAI struct {
	client openai.Client
}

// NewOpenAI refuses an incomplete configuration, so a bad revision fails at startup instead of on
// its first call. Options are forwarded to the SDK, so a test can swap the HTTP client.
//
// The SDK still reads OPENAI_ORG_ID, OPENAI_PROJECT_ID and OPENAI_CUSTOM_HEADERS from the
// environment.
func NewOpenAI(config OpenAIConfig, opts ...option.RequestOption) (*OpenAI, error) {
	if config.BaseURL == "" || config.APIKey == "" {
		return nil, ErrProviderNotConfigured
	}

	opts = append([]option.RequestOption{
		option.WithBaseURL(config.BaseURL), option.WithAPIKey(config.APIKey),
	}, opts...)

	return &OpenAI{client: openai.NewClient(opts...)}, nil
}

// Start posts the caller's payload in the Async mode.
//
// background makes crash-safety possible: the call returns an identifier immediately, so a restarted
// check resumes an operation already paid for. stream is off, because the result is read back by
// polling.
func (provider *OpenAI) Start(ctx context.Context, request *AsyncStartRequest) (*ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "lib.OpenAI.Start")
	defer span.End()

	body, err := asyncEnvelope(request.Payload)
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

	response := &responses.Response{}

	err = provider.client.Post(ctx, responsesPath, body, response, option.WithMaxRetries(0))
	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIStartError(err, sent.Load()))
	}

	return providerCallOf(response), nil
}

// Get reads an operation, and is also the re-attach path.
func (provider *OpenAI) Get(ctx context.Context, id string) (*ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "lib.OpenAI.Get")
	defer span.End()

	span.SetAttributes(attribute.String("provider.call_id", id))

	response, err := provider.client.Responses.Get(ctx, id, responses.ResponseGetParams{}, readOptions...)
	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIError(err))
	}

	return providerCallOf(response), nil
}

// Cancel stops an operation so an abandoned generation stops costing.
func (provider *OpenAI) Cancel(ctx context.Context, id string) (*ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "lib.OpenAI.Cancel")
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

// asyncEnvelope writes the fields the Async mode owns over the caller's payload. The other values
// are forwarded as sent: they are decoded only as far as the top-level object, and re-encoded
// without escaping.
func asyncEnvelope(payload json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage

	err := json.Unmarshal(payload, &fields)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	fields["background"] = json.RawMessage("true")
	fields["stream"] = json.RawMessage("false")

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
func providerCallOf(response *responses.Response) *ProviderCall {
	call := &ProviderCall{ID: response.ID, State: ProviderCallRunning}

	switch response.Status {
	case responses.ResponseStatusCompleted, responses.ResponseStatusIncomplete:
		call.State = ProviderCallSucceeded
	case responses.ResponseStatusFailed:
		call.State = ProviderCallFailed
		call.Reason = response.Error.Message
		call.Retryable = retryableFailure(response.Error.Code)
	case responses.ResponseStatusCancelled:
		call.State = ProviderCallCancelled
	case responses.ResponseStatusQueued, responses.ResponseStatusInProgress:
	default:
		// An unknown status is treated as still running rather than terminal: polling again is
		// cheap, and settling on a status we do not understand throws away work already paid for.
	}

	if !call.State.Terminal() {
		return call
	}

	call.Response = json.RawMessage(response.RawJSON())
	call.Model = response.Model
	call.ServiceTier = string(response.ServiceTier)

	// Zero tokens is a real answer and not a missing one, so presence is decided by the provider
	// reporting the field.
	if response.Usage.JSON.TotalTokens.Valid() {
		call.Usage = &ProviderUsage{
			InputTokens:       response.Usage.InputTokens,
			CachedInputTokens: response.Usage.InputTokensDetails.CachedTokens,
			CacheWriteTokens:  response.Usage.InputTokensDetails.CacheWriteTokens,
			OutputTokens:      response.Usage.OutputTokens,
			ReasoningTokens:   response.Usage.OutputTokensDetails.ReasoningTokens,
		}
	}

	return call
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
			return fmt.Errorf("%w: %w", ErrProviderRetryable, err)
		}

		return fmt.Errorf("%w: %w", ErrProviderStartAmbiguous, err)
	}

	err = withProviderMessage(err, apiErr)

	switch status := apiErr.StatusCode; {
	case status == http.StatusTooManyRequests && apiErr.Code == "insufficient_quota":
		// Retrying cannot help until an operator adds credit, so the caller gets an answer instead.
		return &ProviderRejectionError{Response: errorBody(apiErr), Err: err}
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", ErrProviderRetryable, err)
	case status == http.StatusRequestTimeout, status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %w", ErrProviderStartAmbiguous, err)
	}

	// The request itself, the credentials, the permissions or the model: nothing ran.
	return &ProviderRejectionError{Response: errorBody(apiErr), Err: err}
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
		return fmt.Errorf("%w: %w", ErrProviderRetryable, err)
	}

	err = withProviderMessage(err, apiErr)

	switch status := apiErr.StatusCode; {
	case status == http.StatusTooManyRequests,
		status == http.StatusRequestTimeout,
		status == http.StatusUnauthorized,
		status == http.StatusForbidden,
		status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %w", ErrProviderRetryable, err)
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
