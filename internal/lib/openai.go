package lib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
)

// ProviderNameOpenAI identifies this provider on the usage record.
const ProviderNameOpenAI = "openai"

// responsesPath is the endpoint requests are posted to.
const responsesPath = "responses"

// ErrTierNotBound is returned when the adapter has no binding for a Tier.
var ErrTierNotBound = errors.New("tier has no model binding")

// OpenAI talks to the Responses API.
type OpenAI struct {
	client openai.Client
	tiers  map[Tier]TierBinding
}

// NewOpenAI builds an adapter for the given Tier bindings. Options are forwarded to the SDK, so a
// test points it at a scripted server with option.WithBaseURL.
func NewOpenAI(tiers map[Tier]TierBinding, opts ...option.RequestOption) (*OpenAI, error) {
	for _, tier := range Tiers {
		binding, ok := tiers[tier]
		if !ok || binding.Model == "" || binding.MaxOutputTokens <= 0 {
			return nil, fmt.Errorf("%w: %s", ErrTierNotBound, tier)
		}
	}

	return &OpenAI{client: openai.NewClient(opts...), tiers: tiers}, nil
}

func (provider *OpenAI) Name() string { return ProviderNameOpenAI }

// openAIRequest is the Responses API request this service sends. It is posted as raw JSON rather
// than through the SDK's typed parameters, which take the schema as a map and would reorder its
// properties; the output follows the schema's order, so the caller's order must survive.
//
//nolint:tagliatelle // OpenAI owns these snake_case fields.
type openAIRequest struct {
	Model            string            `json:"model"`
	Instructions     string            `json:"instructions"`
	Input            string            `json:"input"`
	Reasoning        *openAIReasoning  `json:"reasoning,omitempty"`
	MaxOutputTokens  int64             `json:"max_output_tokens"`
	Text             openAIText        `json:"text"`
	Background       bool              `json:"background"`
	Store            bool              `json:"store"`
	Metadata         map[string]string `json:"metadata"`
	SafetyIdentifier string            `json:"safety_identifier"`
}

type openAIReasoning struct {
	Effort string `json:"effort"`
}

type openAIText struct {
	Format openAIFormat `json:"format"`
}

type openAIFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

// Start composes the request from the Tier's binding and posts it.
//
// background makes crash-safety possible: the call returns an identifier immediately, so a restarted
// check resumes an operation already paid for. store keeps the provider from retaining user content,
// and leaves re-attach intact because the result is read back well inside the polling window.
// metadata identifies an operation orphaned between the provider accepting a call and its identifier
// reaching the database.
func (provider *OpenAI) Start(ctx context.Context, request *ProviderStartRequest) (*ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "lib.OpenAI.Start")
	defer span.End()

	binding, ok := provider.tiers[request.Tier]
	if !ok {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %s", ErrTierNotBound, request.Tier))
	}

	span.SetAttributes(attribute.String("provider.model", binding.Model))

	body := &openAIRequest{
		Model:           binding.Model,
		Instructions:    request.Instructions,
		Input:           string(request.Input),
		MaxOutputTokens: binding.MaxOutputTokens,
		Text: openAIText{Format: openAIFormat{
			Type: "json_schema", Name: "output", Schema: request.OutputSchema, Strict: true,
		}},
		Background: true,
		Store:      false,
		Metadata: map[string]string{
			"generation_id": request.GenerationID,
			"attempt":       strconv.Itoa(int(request.Attempt)),
		},
		SafetyIdentifier: safetyIdentifier(request.EndUserID),
	}

	if binding.ReasoningEffort != "" {
		body.Reasoning = &openAIReasoning{Effort: binding.ReasoningEffort}
	}

	response := &responses.Response{}

	err := provider.client.Post(ctx, responsesPath, body, response, option.WithMaxRetries(0))
	if err != nil {
		return nil, otel.ReportError(span, classifyOpenAIStartError(err))
	}

	return providerCallOf(response), nil
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

	return providerCallOf(response), nil
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

	return providerCallOf(response), nil
}

// safetyIdentifier hashes the end user, as OpenAI asks: the provider attributes abuse to a stable
// identifier without learning the platform's own.
func safetyIdentifier(endUserID string) string {
	digest := sha256.Sum256([]byte("a-novel/genai/safety-identifier/v1:" + endUserID))

	return hex.EncodeToString(digest[:])
}

// providerCallOf reads the fields this service records off a provider response.
func providerCallOf(response *responses.Response) *ProviderCall {
	call := &ProviderCall{
		ID:              response.ID,
		State:           ProviderCallRunning,
		Model:           response.Model,
		ReasoningEffort: string(response.Reasoning.Effort),
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

	switch response.Status {
	case responses.ResponseStatusCompleted:
		completed(call, response)
	case responses.ResponseStatusIncomplete:
		call.State = ProviderCallFailed
		call.Reason = response.IncompleteDetails.Reason
		call.Failure = incompleteFailure(response.IncompleteDetails.Reason)
	case responses.ResponseStatusFailed:
		call.State = ProviderCallFailed
		call.Reason = response.Error.Message
		call.Failure, call.Retryable = failedFailure(response.Error.Code)
	case responses.ResponseStatusCancelled:
		call.State = ProviderCallCancelled
	case responses.ResponseStatusQueued, responses.ResponseStatusInProgress:
	default:
		// An unknown status is treated as still running rather than terminal: polling again is
		// cheap, and settling on a status we do not understand throws away work already paid for.
	}

	return call
}

// completed reads a finished response: the schema document, or the refusal that replaced it.
func completed(call *ProviderCall, response *responses.Response) {
	call.State = ProviderCallFailed

	for _, item := range response.Output {
		for _, content := range item.Content {
			if content.Type == "refusal" {
				call.Reason = content.Refusal
				call.Failure = &Failure{Kind: FailureRefused, Message: "the model refused the request"}

				return
			}
		}
	}

	output := json.RawMessage(strings.TrimSpace(response.OutputText()))

	var document map[string]json.RawMessage

	err := json.Unmarshal(output, &document)
	if err != nil {
		call.Reason = "output is not a JSON object"
		call.Failure = &Failure{Kind: FailureFailed, Message: "the provider returned an output that is not a JSON object"}

		return
	}

	call.State = ProviderCallSucceeded
	call.Output = output
}

func incompleteFailure(reason string) *Failure {
	switch reason {
	case "max_output_tokens":
		return &Failure{Kind: FailureIncomplete, Message: "the output reached the tier's output token ceiling"}
	case "content_filter":
		return &Failure{Kind: FailureRefused, Message: "the provider's content filter stopped the output"}
	default:
		return &Failure{Kind: FailureIncomplete, Message: "the provider stopped the output early"}
	}
}

// failedFailure maps a provider failure onto a kind, and whether another attempt is worth paying for.
func failedFailure(code responses.ResponseErrorCode) (*Failure, bool) {
	switch code {
	case responses.ResponseErrorCodeServerError,
		responses.ResponseErrorCodeRateLimitExceeded,
		responses.ResponseErrorCodeVectorStoreTimeout:
		return &Failure{Kind: FailureFailed, Message: "the provider failed to complete the call"}, true
	case responses.ResponseErrorCodeInvalidPrompt,
		responses.ResponseErrorCodeBioPolicy,
		responses.ResponseErrorCodeMisalignmentPolicyViolation:
		return &Failure{Kind: FailureRefused, Message: "the provider's usage policy refused the request"}, false
	default:
		return &Failure{Kind: FailureFailed, Message: "the provider failed the call"}, false
	}
}

// classifyOpenAIStartError separates definitive rejection from a response that may have been lost.
func classifyOpenAIStartError(err error) error {
	// A failed dial proves the request never left: no connection, so nothing reached the provider.
	var dialErr *net.OpError
	if errors.As(err, &dialErr) && dialErr.Op == "dial" {
		return fmt.Errorf("%w: %w", ErrProviderRetryable, err)
	}

	var apiErr *openai.Error

	if !errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %w", ErrProviderStartAmbiguous, err)
	}

	switch status := apiErr.StatusCode; {
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", ErrProviderRetryable, err)
	case status == http.StatusRequestTimeout, status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %w", ErrProviderStartAmbiguous, err)
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		return &ProviderRejectionError{Failure: invalidRequestFailure(apiErr), Err: err}
	}

	// Credentials, permissions or an unknown model: this service's configuration, not the caller's.
	return &ProviderRejectionError{
		Failure: Failure{Kind: FailureFailed, Message: "the provider refused the call"}, Err: err,
	}
}

func invalidRequestFailure(apiErr *openai.Error) Failure {
	switch {
	case apiErr.Code == "context_length_exceeded":
		return Failure{Kind: FailureInvalidRequest, Message: "the input exceeds the model's context window"}
	case strings.HasPrefix(apiErr.Param, "text.format"):
		return Failure{Kind: FailureInvalidRequest, Message: "the provider rejected the output schema"}
	default:
		return Failure{Kind: FailureInvalidRequest, Message: "the provider rejected the request"}
	}
}

// classifyOpenAIError decides whether reading an operation again is worth it.
//
// Retryable: transport failures, rate limits, and provider-side faults. Anything else, a not-found
// above all, means the operation cannot be read.
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
