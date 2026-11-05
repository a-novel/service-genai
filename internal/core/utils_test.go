package core_test

import (
	"encoding/json"
	"errors"
	"iter"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
	connectorsmocks "github.com/a-novel/service-genai/internal/lib/connectors/mocks"
)

var errFoo = errors.New("foo")

const (
	testCallID = "resp_1"
	// testProvider is the provider the generations under test run on.
	testProvider = "openai"
	// testStreamer is a provider offering the Streaming mode too.
	testStreamer = "streamer"
	testPurpose  = "studio.generation"
	// testModel is the snapshot a provider reports running.
	testModel = "a-model-2026-01-01"
)

var (
	// testPayload is a generation's provider request as the caller built it.
	testPayload = json.RawMessage(`{"model":"a-model","input":"Continue the scene & more."}`)
	// testResponse is the provider's answer to a call, as it sent it.
	testResponse = json.RawMessage(`{"id":"resp_1","status":"completed","usage":{"output_tokens":5}}`)
)

var (
	testOwner        = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	testGenerationID = uuid.MustParse("01999999-0000-7000-8000-000000000001")
	// testNow stands in for the database clock as the generation was last read.
	testNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// testCaller is the span of the request a test acts for.
	testCaller = trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
	})
)

// pendingGeneration has no start intent, as an earlier release left a generation between two starts.
func pendingGeneration() *dao.Generation {
	return &dao.Generation{
		ID:        testGenerationID,
		OwnerID:   testOwner,
		Purpose:   testPurpose,
		Provider:  testProvider,
		Status:    dao.GenerationStatusPending,
		ReadAt:    testNow,
		CreatedAt: testNow,
	}
}

// startingGeneration recorded start intent the given age ago, with no call id yet, as a submit
// records it.
func startingGeneration(age time.Duration) *dao.Generation {
	generation := pendingGeneration()
	requested := testNow.Add(-age)
	generation.StartRequestedAt = &requested

	return generation
}

// runningGeneration has a known provider call.
func runningGeneration() *dao.Generation {
	generation := startingGeneration(time.Minute)
	callID := testCallID
	generation.Status = dao.GenerationStatusRunning
	generation.ProviderCallID = &callID

	return generation
}

// withCancel marks the generation for cancellation.
func withCancel(generation *dao.Generation) *dao.Generation {
	requested := testNow
	generation.CancelRequestedAt = &requested

	return generation
}

// cancelledAgo dates the owner's cancel request the given age before testNow.
func cancelledAgo(generation *dao.Generation, age time.Duration) *dao.Generation {
	requested := testNow.Add(-age)
	generation.CancelRequestedAt = &requested

	return generation
}

// startedAgo backdates a generation's start by the given age before testNow.
func startedAgo(generation *dao.Generation, age time.Duration) *dao.Generation {
	started := testNow.Add(-age)
	generation.StartRequestedAt = &started

	return generation
}

// streamed marks a generation submitted for streaming.
func streamed(generation *dao.Generation) *dao.Generation {
	generation.Streamed = true

	return generation
}

// asyncStreaming serves both the Async and the Streaming modes, as the OpenAI connector does.
type asyncStreaming struct {
	*connectorsmocks.MockAsync
	*connectorsmocks.MockStreaming
}

// withProvider moves the generation to another provider.
func withProvider(generation *dao.Generation, provider string) *dao.Generation {
	generation.Provider = provider

	return generation
}

// settledGeneration landed in the given terminal status.
func settledGeneration(status dao.GenerationStatus) *dao.Generation {
	generation := runningGeneration()
	settled := testNow
	expires := testNow.Add(time.Hour)
	generation.Status = status
	generation.SettledAt = &settled
	generation.ExpiresAt = &expires

	return generation
}

// withoutCall drops the generation's provider call, as for a start settled before its id landed.
func withoutCall(generation *dao.Generation) *dao.Generation {
	generation.ProviderCallID = nil

	return generation
}

// providerCall is the provider's view of the call, with its answer once terminal.
func providerCall(state connectors.CallState) *connectors.Call {
	call := &connectors.Call{ID: testCallID, State: state}
	if state.Terminal() {
		call.Response = testResponse
		call.Model = testModel
		call.Class = "high"
		call.Usage = &connectors.Usage{InputTokens: 1000, CachedInputTokens: 200, OutputTokens: 500, ReasoningTokens: 100}
	}

	return call
}

// failedCall is a call the provider reports failed.
func failedCall() *connectors.Call {
	call := providerCall(connectors.CallFailed)
	call.Reason = "the model failed"

	return call
}

// testAttemptRows are the provider's answers to a generation's attempts, as stored.
func testAttemptRows() []*dao.GenerationAttempt {
	return []*dao.GenerationAttempt{{GenerationID: testGenerationID, Attempt: 1, Response: testResponse}}
}

// streamOf yields the given events, then the error when there is one, as a connector streams them.
func streamOf(events []*connectors.StreamEvent, err error) iter.Seq2[*connectors.StreamEvent, error] {
	return func(yield func(*connectors.StreamEvent, error) bool) {
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}

		if err != nil {
			yield(nil, err)
		}
	}
}
