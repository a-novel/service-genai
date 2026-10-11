package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

var errFoo = errors.New("foo")

const (
	testCallID = "resp_1"
	// testProvider is the provider the generations under test run on.
	testProvider = "openai"
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

// discardLogger drops what checks log: provider details stay server-side, out of assertions.
type discardLogger struct{}

func (discardLogger) Err(context.Context, string, ...any) {}

// pendingGeneration has never started.
func pendingGeneration() *dao.Generation {
	return &dao.Generation{
		ID:          testGenerationID,
		OwnerID:     testOwner,
		Purpose:     testPurpose,
		Provider:    testProvider,
		Payload:     testPayload,
		Status:      dao.GenerationStatusPending,
		MaxAttempts: 2,
		CheckedAt:   testNow,
	}
}

// startingGeneration recorded start intent for attempt 1 the given age ago, with no call id yet.
func startingGeneration(age time.Duration) *dao.Generation {
	generation := pendingGeneration()
	requested := testNow.Add(-age)
	generation.Attempt = 1
	generation.StartRequestedAt = &requested

	return generation
}

// runningGeneration has a known provider call on the given attempt.
func runningGeneration(attempt int16) *dao.Generation {
	generation := startingGeneration(time.Minute)
	callID := testCallID
	generation.Status = dao.GenerationStatusRunning
	generation.Attempt = attempt
	generation.ProviderCallID = &callID

	return generation
}

// withCancel marks the generation for cancellation.
func withCancel(generation *dao.Generation) *dao.Generation {
	requested := testNow
	generation.CancelRequestedAt = &requested

	return generation
}

// withProvider moves the generation to another provider.
func withProvider(generation *dao.Generation, provider string) *dao.Generation {
	generation.Provider = provider

	return generation
}

// settledGeneration landed in the given terminal status.
func settledGeneration(status dao.GenerationStatus) *dao.Generation {
	generation := runningGeneration(1)
	settled := testNow
	expires := testNow.Add(time.Hour)
	generation.Status = status
	generation.SettledAt = &settled
	generation.ExpiresAt = &expires

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
func failedCall(retryable bool) *connectors.Call {
	call := providerCall(connectors.CallFailed)
	call.Reason = "the model failed"
	call.Retryable = retryable

	return call
}

// testAttemptRows are the provider's answers to a generation's attempts, as stored.
func testAttemptRows() []*dao.GenerationAttempt {
	return []*dao.GenerationAttempt{{GenerationID: testGenerationID, Attempt: 1, Response: testResponse}}
}
