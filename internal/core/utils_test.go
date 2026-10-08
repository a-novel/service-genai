package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
)

var errFoo = errors.New("foo")

const testCallID = "resp_1"

// testStoredRequest is a generation's provider-neutral request as it is stored.
var testStoredRequest = json.RawMessage(
	`{"tier":"balanced","instructions":"Continue.","input":{"scene":"a door"},"outputSchema":{"type":"object"}}`,
)

var (
	testOwner        = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	testGenerationID = uuid.MustParse("01999999-0000-7000-8000-000000000001")
	// testNow stands in for the database clock as the generation was last read.
	testNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
)

// discardLogger drops what checks log: provider details stay server-side, out of assertions.
type discardLogger struct{}

func (discardLogger) Err(context.Context, string, ...any) {}

// pendingGeneration has never started.
func pendingGeneration() *dao.Generation {
	return &dao.Generation{
		ID:          testGenerationID,
		OwnerID:     testOwner,
		Purpose:     "studio.generation",
		Request:     testStoredRequest,
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

func providerCall(state lib.ProviderCallState) *lib.ProviderCall {
	call := &lib.ProviderCall{ID: testCallID, State: state, Model: "a-model-snapshot", ReasoningEffort: "medium"}
	if state.Terminal() {
		call.Usage = &lib.ProviderUsage{InputTokens: 1000, CachedInputTokens: 200, OutputTokens: 500}
	}

	return call
}

// failedCall is a terminal call that ended in the given failure.
func failedCall(failure lib.Failure) *lib.ProviderCall {
	call := providerCall(lib.ProviderCallFailed)
	call.Failure = &failure

	return call
}

// testUsageRows is what a generation's provider calls consumed, as stored.
func testUsageRows() []*dao.GenerationUsage {
	effort := "medium"

	return []*dao.GenerationUsage{
		{
			GenerationID: testGenerationID, Attempt: 1, Provider: "openai", Model: "a-model-snapshot",
			ReasoningEffort: &effort, InputTokens: 1000, CachedInputTokens: 200, OutputTokens: 500,
		},
	}
}

// compact is how a request's JSON reads once stored: whitespace removed, key order kept.
func compact(value json.RawMessage) string {
	var buffer bytes.Buffer

	err := json.Compact(&buffer, value)
	if err != nil {
		panic(err)
	}

	return buffer.String()
}
