package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
)

// ProviderNameOpenAI identifies this provider on the usage record.
const ProviderNameOpenAI = "openai"

// responsesPath is the endpoint the raw request is posted to. The typed params are deliberately
// bypassed — see [OpenAI.Start].
const responsesPath = "responses"

// OpenAI talks to the Responses API.
type OpenAI struct {
	client openai.Client
}

// NewOpenAI builds an adapter. Options are forwarded to the SDK, so a test points it at a scripted
// server with option.WithBaseURL.
func NewOpenAI(opts ...option.RequestOption) *OpenAI {
	return &OpenAI{client: openai.NewClient(opts...)}
}

func (provider *OpenAI) Name() string { return ProviderNameOpenAI }

// Start posts the caller's request, merged with the two fields crash-safety requires.
//
// The request is sent as raw JSON rather than through the SDK's typed parameters. Unmarshalling it
// into those would silently drop any field this SDK version does not know, which is exactly the
// control the caller is supposed to keep — and it would mean a release here every time the provider
// adds a parameter.
func (provider *OpenAI) Start(ctx context.Context, request *ProviderStartRequest) (*ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "lib.OpenAI.Start")
	defer span.End()

	span.SetAttributes(
		attribute.String("provider.generation_id", request.GenerationID),
		attribute.Int("provider.attempt", int(request.Attempt)),
	)

	body, err := mergeProviderFields(request)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	response := new(responses.Response)

	err = provider.client.Post(ctx, responsesPath, body, response)
	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIError(err))
	}

	return otel.ReportSuccess(span, providerCallOf(response)), nil
}

// Get reads an operation, and is also the re-attach path.
func (provider *OpenAI) Get(ctx context.Context, id string) (*ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "lib.OpenAI.Get")
	defer span.End()

	span.SetAttributes(attribute.String("provider.call_id", id))

	response, err := provider.client.Responses.Get(ctx, id, responses.ResponseGetParams{})
	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIError(err))
	}

	return otel.ReportSuccess(span, providerCallOf(response)), nil
}

// Cancel stops an operation so an abandoned generation stops costing.
func (provider *OpenAI) Cancel(ctx context.Context, id string) (*ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "lib.OpenAI.Cancel")
	defer span.End()

	span.SetAttributes(attribute.String("provider.call_id", id))

	response, err := provider.client.Responses.Cancel(ctx, id)
	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIError(err))
	}

	return otel.ReportSuccess(span, providerCallOf(response)), nil
}

// mergeProviderFields adds the three fields this service owns, overwriting whatever the caller set.
//
// background is what makes crash-safety possible: the call returns an identifier immediately, so a
// restarted worker can resume it rather than burning the whole spend. store is false because nothing
// here needs the provider to keep the prose, and a background response stays retrievable for roughly
// ten minutes regardless, where re-attach only has to outlive a pod restart. metadata identifies an
// operation orphaned between the provider accepting a call and its identifier reaching the database.
func mergeProviderFields(request *ProviderStartRequest) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}

	err := json.Unmarshal(request.Request, &fields)
	if err != nil {
		return nil, fmt.Errorf("decode request: %w", err)
	}

	metadata, err := json.Marshal(map[string]string{
		"generation_id": request.GenerationID,
		"attempt":       strconv.Itoa(int(request.Attempt)),
	})
	if err != nil {
		return nil, fmt.Errorf("encode metadata: %w", err)
	}

	fields["background"] = json.RawMessage("true")
	fields["store"] = json.RawMessage("false")
	fields["metadata"] = metadata

	body, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	return body, nil
}

// providerCallOf reads the fields this service records off a provider response, and leaves the rest
// as the opaque output the caller asked for.
func providerCallOf(response *responses.Response) *ProviderCall {
	call := &ProviderCall{
		ID:     response.ID,
		State:  providerCallStateOf(response.Status),
		Model:  response.Model,
		Output: []byte(response.RawJSON()),
	}

	switch call.State {
	case ProviderCallIncomplete:
		call.Reason = response.IncompleteDetails.Reason
	case ProviderCallFailed:
		call.Reason = response.Error.Message
	case ProviderCallRunning, ProviderCallSucceeded, ProviderCallCancelled:
	}

	// Absent while running, and on a failure that never reached the model. Zero tokens is a real
	// answer and not a missing one, so presence is decided by the provider reporting the field.
	if response.Usage.JSON.TotalTokens.Valid() {
		call.Usage = &ProviderUsage{
			InputTokens:       response.Usage.InputTokens,
			CachedInputTokens: response.Usage.InputTokensDetails.CachedTokens,
			OutputTokens:      response.Usage.OutputTokens,
			ReasoningTokens:   response.Usage.OutputTokensDetails.ReasoningTokens,
		}
	}

	return call
}

func providerCallStateOf(status responses.ResponseStatus) ProviderCallState {
	switch status {
	case responses.ResponseStatusCompleted:
		return ProviderCallSucceeded
	case responses.ResponseStatusIncomplete:
		return ProviderCallIncomplete
	case responses.ResponseStatusFailed:
		return ProviderCallFailed
	case responses.ResponseStatusCancelled:
		return ProviderCallCancelled
	case responses.ResponseStatusQueued, responses.ResponseStatusInProgress:
		return ProviderCallRunning
	default:
		// An unknown status is treated as still running rather than terminal: polling again is
		// cheap, and settling on a status we do not understand throws away work already paid for.
		return ProviderCallRunning
	}
}

// classifyOpenAIError decides whether another attempt is worth spending.
//
// Retryable: transport failures, rate limits, and provider-side faults — the request was fine and
// may succeed unchanged. Terminal: anything the provider rejected on its content, because retrying
// only spends an attempt to be rejected again.
func classifyOpenAIError(err error) error {
	var apiErr *openai.Error

	if !errors.As(err, &apiErr) {
		// No HTTP response at all: a dial failure, a timeout, a reset connection.
		return fmt.Errorf("%w: %w", ErrProviderRetryable, err)
	}

	switch status := apiErr.StatusCode; {
	case status == http.StatusTooManyRequests,
		status == http.StatusRequestTimeout,
		status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %w", ErrProviderRetryable, err)
	}

	return fmt.Errorf("provider rejected the request: %w", err)
}
