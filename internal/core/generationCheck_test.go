package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/a-novel-kit/golib/transaction/transactiontest"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
	connectorsmocks "github.com/a-novel/service-genai/internal/lib/connectors/mocks"
)

func TestGenerationCheck(t *testing.T) {
	t.Parallel()

	const retention = time.Hour

	callID := testCallID
	cancelledReason := "generation cancelled"
	unknownMessage := "the provider may have accepted the call, but its outcome is unknown"
	lostMessage := "the provider no longer holds the result"
	startFailedMessage := "the provider call could not be started"
	rejectedMessage := "the provider rejected the request"
	callFailedMessage := "the provider failed the call"
	unavailableMessage := "no connector serves the generation's provider"
	overdueMessage := "the provider did not finish the call in time, so it was stopped"
	notStoppedMessage := "the call could not be stopped, and the provider may still run it"
	tooLargeMessage := "the provider's response is larger than the 24 MiB this service reads"
	notRecordedMessage := "the provider's answer could not be recorded"
	// runCeilingForTest mirrors the run ceiling, past which every call is stopped.
	const runCeilingForTest = 2 * time.Hour

	rejectionBody := json.RawMessage(`{"error":{"message":"bad model"}}`)

	// settle describes the settle a case expects; the runner fills in the id and retention.
	type settle struct {
		attempt int16
		callID  *string
		status  dao.GenerationStatus
		reason  *string
		// outcomeUnknown marks a start the provider may have accepted: the slot stays held.
		outcomeUnknown bool
	}

	testCases := []struct {
		name string

		generation *dao.Generation

		beginStartErr error
		start         *connectors.Call
		startErr      error
		recordErr     error
		// recordRetried lands the second write of a call id the first could not.
		recordRetried bool
		// cancelledInFlight has the owner cancel while the start is in flight, so the recorded call
		// carries the request.
		cancelledInFlight bool
		observe           *connectors.Call
		observeErr        error
		settleErr         error
		attemptErr        error
		usageErr          error
		// callerGone runs the check for a caller whose call already ended.
		callerGone bool
		// expectStop asks the provider to cancel instead of reading the call.
		expectStop bool
		// stopped is the provider's answer to a cancel sent after the read found the call stuck.
		stopped *connectors.Call

		expectStart      bool
		expectRelease    bool
		expectRecord     bool
		expectStopOrphan bool
		expectSettle     *settle
		// expectNotRecorded settles the generation failed without the answer its settle could not record.
		expectNotRecorded *settle
		expectRequeue     bool
		// expectAttempt is the provider answer the attempt records, if any.
		expectAttempt json.RawMessage
		// expectUsage records what the observed call consumed.
		expectUsage  bool
		expectReread bool
		// rereadAs is what the re-read finds, a settled generation unless stated.
		rereadAs *dao.Generation
		// rereadErr fails the re-read.
		rereadErr error

		expectStatus dao.GenerationStatus
		expectErr    error
	}{
		{
			name: "Success/Settled",

			generation: settledGeneration(dao.GenerationStatusSucceeded),

			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			name: "Success/Start",

			generation: pendingGeneration(),
			start:      providerCall(connectors.CallRunning),

			expectStart:  true,
			expectRecord: true,
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The owner cancelled while the start was in flight: the recorded call is stopped at once,
			// rather than running until the next check.
			name: "Success/CancelledDuringStart",

			generation:        pendingGeneration(),
			start:             providerCall(connectors.CallRunning),
			cancelledInFlight: true,
			expectStop:        true,
			observe:           providerCall(connectors.CallCancelled),

			expectStart:  true,
			expectRecord: true,
			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// A streamed start records its call while it holds; the cancel is applied once the hold ends.
			name: "Success/CancelledDuringStreamedStart",

			generation:        streamed(pendingGeneration()),
			start:             providerCall(connectors.CallRunning),
			cancelledInFlight: true,
			expectStop:        true,
			observe:           providerCall(connectors.CallCancelled),

			expectStart:  true,
			expectRecord: true,
			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// The stop's settle failed: the call stays recorded with its cancel, for the next check.
			name: "Error/CancelledDuringStartSettle",

			generation:        pendingGeneration(),
			start:             providerCall(connectors.CallRunning),
			cancelledInFlight: true,
			expectStop:        true,
			observe:           providerCall(connectors.CallCancelled),
			settleErr:         errFoo,

			expectStart:  true,
			expectRecord: true,
			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectErr: errFoo,
		},
		{
			// The provider refused before accepting anything, so the attempt costs nothing and comes back.
			name: "Success/StartRateLimited",

			generation: pendingGeneration(),
			startErr:   connectors.ErrRetryable,

			expectStart:   true,
			expectRelease: true,
			expectStatus:  dao.GenerationStatusPending,
		},
		{
			// A request the provider keeps turning away must not hold a concurrency slot forever.
			name: "Success/StartRetriesExhausted",

			generation: retryingSince(pendingGeneration(), time.Hour),
			startErr:   connectors.ErrRetryable,

			expectStart:  true,
			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &startFailedMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The retries are the attempt's own: one requeued after a long call still gets them.
			name: "Success/RequeuedStartRateLimited",

			generation: submittedAgo(pendingGeneration(), time.Hour),
			startErr:   connectors.ErrRetryable,

			expectStart:   true,
			expectRelease: true,
			expectStatus:  dao.GenerationStatusPending,
		},
		{
			// The connector creates a streamed generation's call in streaming mode.
			name: "Success/StreamedStart",

			generation: streamed(pendingGeneration()),
			start:      providerCall(connectors.CallRunning),

			expectStart:  true,
			expectRecord: true,
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// Nothing ran. The caller built the request, so it reads the provider's reason to fix it.
			name: "Success/StartRejected",

			generation: pendingGeneration(),
			startErr:   &connectors.RejectionError{Response: rejectionBody, Err: errFoo},

			expectStart:   true,
			expectSettle:  &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &rejectedMessage},
			expectAttempt: rejectionBody,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// Recording the rejection failed: the start intent stays for the next check to resolve.
			name: "Error/StartRejectedSettle",

			generation: pendingGeneration(),
			startErr:   &connectors.RejectionError{Response: rejectionBody, Err: errFoo},
			settleErr:  errFoo,

			expectStart:  true,
			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &rejectedMessage},
			expectErr:    errFoo,
		},
		{
			name: "Success/StartFailed",

			generation: pendingGeneration(),
			startErr:   errFoo,

			expectStart:  true,
			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &startFailedMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The call may have been accepted. Starting another could pay twice, so the attempt ends.
			name: "Success/StartAmbiguous",

			generation: pendingGeneration(),
			startErr:   connectors.ErrStartAmbiguous,

			expectStart:  true,
			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &unknownMessage, outcomeUnknown: true},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/StartedByAnotherCheck",

			generation:    pendingGeneration(),
			beginStartErr: dao.ErrGenerationChanged,

			expectReread: true,
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			// The generation settled while the start was in flight: the accepted call belongs to nothing.
			name: "Success/OrphanStopped",

			generation: pendingGeneration(),
			start:      providerCall(connectors.CallRunning),
			recordErr:  dao.ErrGenerationChanged,

			expectStart:      true,
			expectRecord:     true,
			expectStopOrphan: true,
			expectReread:     true,
			expectStatus:     dao.GenerationStatusSucceeded,
		},
		{
			// A deployment dropped the provider's connector: nothing can run the generation any more.
			name: "Success/ProviderUnavailable",

			generation: withProvider(pendingGeneration(), "gone"),

			expectSettle: &settle{status: dao.GenerationStatusFailed, reason: &unavailableMessage},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/ProviderUnavailableWhileRunning",

			generation: withProvider(runningGeneration(1), "gone"),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &unavailableMessage,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/CancelledBeforeStart",

			generation: withCancel(pendingGeneration()),

			expectSettle: &settle{status: dao.GenerationStatusCancelled, reason: &cancelledReason},
			expectStatus: dao.GenerationStatusCancelled,
		},
		{
			// Another process may still be sending this start, so nothing happens yet.
			name: "Success/StartInFlight",

			generation: startingGeneration(10 * time.Second),

			expectStatus: dao.GenerationStatusPending,
		},
		{
			name: "Success/StartOutcomeUnknown",

			generation: startingGeneration(time.Minute),

			expectSettle: &settle{attempt: 1, status: dao.GenerationStatusFailed, reason: &unknownMessage, outcomeUnknown: true},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/StillRunning",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallRunning),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			name: "Success/Succeeded",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A cancelled call is not a free one: its answer is recorded.
			name: "Success/Cancelled",

			generation: withCancel(runningGeneration(1)),
			observe:    providerCall(connectors.CallCancelled),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// Another check stopped the call on its owner's request after this one read the generation.
			name: "Success/CancelledMeanwhile",

			generation:   runningGeneration(1),
			observe:      providerCall(connectors.CallCancelled),
			expectReread: true,
			// Moved on since: the settle stays fenced on the attempt this check observed.
			rereadAs: withCancel(runningGeneration(2)),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			name: "Success/ProviderFailed",

			generation: runningGeneration(1),
			observe:    failedCall(false),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			name: "Success/RetryableFailure",

			generation: runningGeneration(1),
			observe:    failedCall(true),

			expectRequeue: true,
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusPending,
		},
		{
			// The owner asked to stop: a fresh attempt would run what they cancelled.
			name: "Success/RetryableFailureAfterCancel",

			generation: withCancel(runningGeneration(1)),
			observe:    failedCall(true),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// A poll that elected the check stamped the generation: a check cut by the caller's
			// deadline would leave it stale, so it runs to the end on its own.
			name: "Success/CallerGone",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			callerGone: true,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// Past the run ceiling, the call is stopped and the generation fails with what it consumed.
			name: "Success/Overdue",

			generation: startedAgo(runningGeneration(1), 3*time.Hour),
			observe:    providerCall(connectors.CallCancelled),
			expectStop: true,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &overdueMessage,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// The call finished before the stop reached it: its result stands.
			name: "Success/OverdueFinishedMeanwhile",

			generation: startedAgo(runningGeneration(1), 3*time.Hour),
			observe:    providerCall(connectors.CallSucceeded),
			expectStop: true,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A provider that cannot be reached, a revoked key included, must not hold the slot forever.
			// The call may still run, so the generation keeps its slot a while, as for an unknown start.
			name: "Success/OverdueUnreachable",

			generation: startedAgo(runningGeneration(1), 3*time.Hour),
			observeErr: connectors.ErrRetryable,
			expectStop: true,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// Just past the ceiling, a stop that fails is asked again before the call is given up on.
			name: "Success/OverdueStopRetried",

			generation: startedAgo(runningGeneration(1), runCeilingForTest+time.Minute),
			observeErr: connectors.ErrRetryable,
			expectStop: true,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A stop the provider never applies ends the generation too, which keeps its slot a while.
			name: "Success/OverdueStopNotApplied",

			generation: startedAgo(runningGeneration(1), 3*time.Hour),
			observe:    providerCall(connectors.CallRunning),
			expectStop: true,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// A stuck streamed call the stop has not reached yet is asked again by the next check.
			name: "Success/StreamedStuckQueuedStopNotApplied",

			generation: streamed(startedAgo(runningGeneration(1), 10*time.Minute)),
			observe:    providerCall(connectors.CallQueued),
			stopped:    providerCall(connectors.CallQueued),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The window counts from the stop at 5 minutes, not from the run ceiling: a stuck streamed
			// call the stop never reaches is given up on around 15 minutes.
			name: "Success/StreamedStuckQueuedGivenUp",

			generation: streamed(startedAgo(runningGeneration(1), 20*time.Minute)),
			observe:    providerCall(connectors.CallQueued),
			stopped:    providerCall(connectors.CallQueued),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// An owner's cancel that keeps failing is given up on 10 minutes after the request, far from
			// the run ceiling.
			name: "Success/CancelGivenUp",

			generation: cancelledAgo(startedAgo(runningGeneration(1), 30*time.Minute), 20*time.Minute),
			observeErr: connectors.ErrRetryable,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The ceiling bounds a call its owner cancelled too: a stop that keeps failing ends there.
			name: "Success/CancelledPastCeilingUnreachable",

			generation: withCancel(startedAgo(runningGeneration(1), 3*time.Hour)),
			observeErr: connectors.ErrRetryable,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// Within the ceiling, a stop that fails is asked again by the next check.
			name: "Success/CancelRetried",

			generation: withCancel(runningGeneration(1)),
			observeErr: connectors.ErrRetryable,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A streamed creation stream left early can leave the call queued for good: past its
			// ceiling, it is stopped like an overdue one.
			name: "Success/StreamedStuckQueued",

			generation: streamed(startedAgo(runningGeneration(1), 10*time.Minute)),
			observe:    providerCall(connectors.CallQueued),
			stopped:    providerCall(connectors.CallCancelled),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &overdueMessage,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			name: "Success/StreamedQueuedWithinCeiling",

			generation: streamed(startedAgo(runningGeneration(1), time.Minute)),
			observe:    providerCall(connectors.CallQueued),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// Only a streamed creation risks the early-disconnect bug; a polled call may queue a while.
			name: "Success/PolledQueuedLong",

			generation: startedAgo(runningGeneration(1), 10*time.Minute),
			observe:    providerCall(connectors.CallQueued),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A streamed generation is one call: a fresh one would restart the stream's numbering.
			name: "Success/StreamedRetryableFailure",

			generation: streamed(runningGeneration(1)),
			observe:    failedCall(true),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			name: "Success/RetriesExhausted",

			generation: runningGeneration(2),
			observe:    failedCall(true),

			expectSettle: &settle{
				attempt: 2, callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// The next check asks again; a provider hiccup is not the generation's failure.
			name: "Success/TransientObservationFailure",

			generation: runningGeneration(1),
			observeErr: connectors.ErrRetryable,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// Gone past the provider's retention window. Starting again would pay twice.
			name: "Success/CallLost",

			generation: runningGeneration(1),
			observeErr: errFoo,

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &lostMessage,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// No read can ever get the call's answer, nor tell whether it still runs: the slot stays held.
			name: "Success/ResponseTooLarge",

			generation: runningGeneration(1),
			observeErr: fmt.Errorf("%w: %w", connectors.ErrResponseTooLarge, errFoo),

			expectSettle: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &tooLargeMessage,
				outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/SettledByAnotherCheck",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			settleErr:  dao.ErrGenerationChanged,

			expectSettle: &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectReread: true,
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			// The write failed but landed: the generation carries the call, which keeps running.
			name: "Success/RecordLanded",

			generation: pendingGeneration(),
			start:      providerCall(connectors.CallRunning),
			recordErr:  errFoo,

			expectStart:  true,
			expectRecord: true,
			expectReread: true,
			rereadAs:     runningGeneration(1),
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A streamed start keeps holding the call its late write recorded: leaving the stream while
			// the call is queued can leave it queued for good.
			name: "Success/StreamedRecordLanded",

			generation: streamed(pendingGeneration()),
			start:      providerCall(connectors.CallQueued),
			recordErr:  errFoo,

			expectStart:  true,
			expectRecord: true,
			expectReread: true,
			rereadAs:     runningGeneration(1),
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A generation that cannot be read may carry the call: it is left running, and the next
			// check finds out.
			name: "Error/RecordUnreadable",

			generation: pendingGeneration(),
			start:      providerCall(connectors.CallRunning),
			recordErr:  errFoo,
			rereadErr:  errFoo,

			expectStart:  true,
			expectRecord: true,
			expectReread: true,
			expectErr:    errFoo,
		},
		{
			// The first write got no answer; the second lands, and the call keeps running.
			name: "Success/RecordRetried",

			generation:    pendingGeneration(),
			start:         providerCall(connectors.CallRunning),
			recordErr:     errFoo,
			recordRetried: true,

			expectStart:  true,
			expectRecord: true,
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The write failed and never landed: nothing tracks the accepted call, so it is stopped.
			name: "Error/RecordLost",

			generation: pendingGeneration(),
			start:      providerCall(connectors.CallRunning),
			recordErr:  errFoo,

			expectStart:      true,
			expectRecord:     true,
			expectReread:     true,
			rereadAs:         startingGeneration(time.Second),
			expectStopOrphan: true,
			expectErr:        errFoo,
		},
		{
			// Recorded while the hold goes on: a write that never landed stops the call there too.
			name: "Error/StreamedRecordLost",

			generation: streamed(pendingGeneration()),
			start:      providerCall(connectors.CallQueued),
			recordErr:  errFoo,

			expectStart:      true,
			expectRecord:     true,
			expectReread:     true,
			rereadAs:         startingGeneration(time.Second),
			expectStopOrphan: true,
			expectErr:        errFoo,
		},
		{
			name: "Error/Settle",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			settleErr:  errFoo,

			expectSettle: &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectErr:    errFoo,
		},
		{
			// The same usage recorded by an earlier settle that did not commit its status is counted once.
			name: "Success/UsageAlreadyRecorded",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			usageErr:   dao.ErrGenerationUsageExists,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A billed call nothing counts would leak past every cap: the settle waits for its usage.
			name: "Error/Usage",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			usageErr:   errFoo,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectErr:     errFoo,
		},
		{
			// An answer the database refuses must not hold the generation running for as long as the
			// provider keeps it: once a stop would have been given up on, it fails without the answer.
			name: "Success/AnswerNotRecorded",

			generation: startedAgo(runningGeneration(1), 3*time.Hour),
			observe:    providerCall(connectors.CallSucceeded),
			expectStop: true,
			attemptErr: errors.Join(errFoo, dao.ErrRefused),

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectNotRecorded: &settle{
				attempt: 1, callID: &callID, status: dao.GenerationStatusFailed, reason: &notRecordedMessage,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// A failure the database may not repeat, such as a lock wait, keeps the paid answer for the
			// next check, however long the call ran.
			name: "Error/AnswerNotRecordedYet",

			generation: startedAgo(runningGeneration(1), 3*time.Hour),
			observe:    providerCall(connectors.CallSucceeded),
			expectStop: true,
			attemptErr: errFoo,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectErr:     errFoo,
		},
		{
			// The settle and its answer commit together, or not at all.
			name: "Error/Attempt",

			generation: runningGeneration(1),
			observe:    providerCall(connectors.CallSucceeded),
			attemptErr: errFoo,

			expectSettle:  &settle{attempt: 1, callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectErr:     errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			generation := testCase.generation

			connector := connectorsmocks.NewMockAsync(t)
			beginStart := coremocks.NewMockGenerationCheckDaoBeginStart(t)
			releaseStart := coremocks.NewMockGenerationCheckDaoReleaseStart(t)
			record := coremocks.NewMockGenerationCheckDaoRecordProviderCall(t)
			settleDao := coremocks.NewMockGenerationCheckDaoSettle(t)
			requeue := coremocks.NewMockGenerationCheckDaoRequeue(t)
			attempt := coremocks.NewMockGenerationCheckDaoAttemptInsert(t)
			usage := coremocks.NewMockGenerationCheckDaoUsageInsert(t)
			get := coremocks.NewMockGenerationCheckDaoGet(t)

			// The attempt a fresh start takes.
			intent := startingGeneration(0)
			intent.CreatedAt = generation.CreatedAt
			intent.Streamed = generation.Streamed
			// The attempt's first start survives a released one, as the intent's write keeps it.
			intent.FirstStartRequestedAt = lo.CoalesceOrEmpty(generation.FirstStartRequestedAt, intent.StartRequestedAt)

			// usageOf is the Usage Record of a call answered for an attempt. The test provider reports
			// no start, so the call is timed from when it was sent.
			usageOf := func(attempt *dao.Generation, call *connectors.Call) *dao.GenerationUsageInsertRequest {
				return &dao.GenerationUsageInsertRequest{
					GenerationID:      attempt.ID,
					Attempt:           attempt.Attempt,
					OwnerID:           attempt.OwnerID,
					Purpose:           attempt.Purpose,
					Provider:          attempt.Provider,
					Model:             call.Model,
					Class:             call.Class,
					StartedAt:         *attempt.StartRequestedAt,
					InputTokens:       call.Usage.InputTokens,
					CachedInputTokens: call.Usage.CachedInputTokens,
					OutputTokens:      call.Usage.OutputTokens,
					ReasoningTokens:   call.Usage.ReasoningTokens,
				}
			}

			if testCase.expectStart || testCase.beginStartErr != nil {
				beginStart.EXPECT().
					Exec(mock.Anything, &dao.GenerationBeginStartRequest{ID: generation.ID}).
					Return(intent, testCase.beginStartErr)
			}

			if testCase.expectStart {
				// The payload as the caller built it: the connector writes its own envelope.
				connector.EXPECT().
					Start(mock.Anything, mock.MatchedBy(func(request *connectors.AsyncStartRequest) bool {
						return bytes.Equal(request.Payload, testPayload) && request.Stream == generation.Streamed
					})).
					RunAndReturn(func(_ context.Context, request *connectors.AsyncStartRequest) (*connectors.Call, error) {
						// A streamed start names its call while it holds, before it returns.
						if testCase.start != nil && generation.Streamed {
							err := request.Accepted(testCase.start)
							if err != nil {
								return nil, err
							}
						}

						return testCase.start, testCase.startErr
					})
			}

			if testCase.expectRelease {
				releaseStart.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationReleaseStartRequest) bool {
						return request.ID == generation.ID && request.Attempt == intent.Attempt && request.Delay > 0
					})).
					Return(pendingGeneration(), nil)
			}

			if testCase.expectRecord {
				recordRequest := &dao.GenerationRecordProviderCallRequest{
					ID: generation.ID, Attempt: intent.Attempt, ProviderCallID: testCallID,
				}

				// The intent's attempt, now running its call.
				recorded := runningGeneration(intent.Attempt)
				recorded.StartRequestedAt = intent.StartRequestedAt

				if testCase.cancelledInFlight {
					recorded = withCancel(recorded)
				}

				record.EXPECT().Exec(mock.Anything, recordRequest).Return(recorded, testCase.recordErr).Once()

				// A write that failed without an answer is tried once more.
				if testCase.recordErr != nil && !errors.Is(testCase.recordErr, dao.ErrGenerationChanged) {
					record.EXPECT().Exec(mock.Anything, recordRequest).
						Return(recorded, lo.Ternary(testCase.recordRetried, nil, testCase.recordErr)).Once()
				}
			}

			if testCase.expectStopOrphan {
				orphan := providerCall(connectors.CallCancelled)

				connector.EXPECT().Cancel(mock.Anything, testCallID).Return(orphan, nil)
				// The orphan ran on the provider's side: what it consumed still counts.
				usage.EXPECT().Exec(mock.Anything, usageOf(intent, orphan)).Return(&dao.GenerationUsage{}, nil)
			}

			if testCase.observe != nil || testCase.observeErr != nil {
				if generation.CancelRequestedAt != nil || testCase.expectStop {
					connector.EXPECT().Cancel(mock.Anything, testCallID).Return(testCase.observe, testCase.observeErr)
				} else {
					// The read must run on a live context, whatever became of the caller's.
					connector.EXPECT().Get(mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil }), testCallID).
						Return(testCase.observe, testCase.observeErr)
				}
			}

			if testCase.stopped != nil {
				connector.EXPECT().Cancel(mock.Anything, testCallID).Return(testCase.stopped, nil)
			}

			expectSettle := func(expected *settle, err error) {
				// A settle is fenced on the start it read: the fresh intent's once one was taken here.
				started := generation.StartRequestedAt
				if testCase.expectStart {
					started = intent.StartRequestedAt
				}

				settleDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationSettleRequest{
						ID:               generation.ID,
						Attempt:          expected.attempt,
						StartRequestedAt: started,
						ProviderCallID:   expected.callID,
						Status:           expected.status,
						Error:            expected.reason,
						Retention:        retention,
						OutcomeUnknown:   expected.outcomeUnknown,
					}).
					Return(settledGeneration(expected.status), err)
			}

			if testCase.expectSettle != nil {
				expectSettle(testCase.expectSettle, testCase.settleErr)
			}

			if testCase.expectNotRecorded != nil {
				expectSettle(testCase.expectNotRecorded, nil)
			}

			if testCase.expectRequeue {
				requeue.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationRequeueRequest) bool {
						return request.ID == generation.ID && request.Attempt == generation.Attempt &&
							request.ProviderCallID == testCallID && request.Delay > 0
					})).
					Return(pendingGeneration(), nil)
			}

			if testCase.expectAttempt != nil {
				number := generation.Attempt
				if testCase.expectStart {
					number = intent.Attempt
				}

				attempt.EXPECT().
					Exec(mock.Anything, &dao.GenerationAttemptInsertRequest{
						GenerationID: generation.ID, Attempt: number, Response: testCase.expectAttempt,
					}).
					Return(&dao.GenerationAttempt{}, testCase.attemptErr)
			}

			if testCase.expectUsage {
				// A call this check started is metered on the intent's attempt.
				metered := generation
				if testCase.expectStart {
					metered = intent
				}

				usage.EXPECT().
					Exec(mock.Anything, usageOf(metered, lo.CoalesceOrEmpty(testCase.stopped, testCase.observe))).
					Return(&dao.GenerationUsage{}, testCase.usageErr)
			}

			if testCase.expectReread {
				reread := lo.CoalesceOrEmpty(testCase.rereadAs, settledGeneration(dao.GenerationStatusSucceeded))
				if testCase.rereadErr != nil {
					reread = nil
				}

				get.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{ID: generation.ID, OwnerID: generation.OwnerID}).
					Return(reread, testCase.rereadErr)
			}

			logger := coremocks.NewMockGenerationCheckLogger(t)
			// Provider details stay server-side, out of assertions.
			logger.EXPECT().Err(mock.Anything, mock.Anything).Maybe()

			check := core.NewGenerationCheck(
				beginStart, releaseStart, record, settleDao, requeue, attempt, usage, get,
				connectors.Registry{testProvider: connector},
				transactiontest.NewTransactor(),
				logger,
				config.Generations{Retention: retention},
			)

			ctx := t.Context()

			if testCase.callerGone {
				var cancelCaller context.CancelFunc

				ctx, cancelCaller = context.WithCancel(ctx)
				cancelCaller()
			}

			result, err := check.Exec(ctx, &core.GenerationCheckRequest{Generation: generation})
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.expectStatus, result.Status)
			}

			for _, assertable := range []interface{ AssertExpectations(t mock.TestingT) bool }{
				connector, beginStart, releaseStart, record, settleDao, requeue, attempt, usage, get,
			} {
				assertable.AssertExpectations(t)
			}
		})
	}
}

//nolint:paralleltest // Replaces the process-wide tracer provider while checks run.
func TestGenerationCheckTelemetry(t *testing.T) {
	submitter := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	inCallerTrace := fmt.Sprintf("00-%s-%s-01", testCaller.TraceID(), trace.SpanID{2})

	testCases := []struct {
		name string

		traceparent *string

		expectLink *string
	}{
		{
			// The sweep, or a poll from another request, links back to the submission.
			name: "Success/LinksTheSubmittingRequest",

			traceparent: &submitter,

			expectLink: new("0af7651916cd43dd8448eb211c80319c"),
		},
		{
			// The check already runs in the submitting trace, so a link would point at itself.
			name: "Success/SameTrace",

			traceparent: &inCallerTrace,
		},
		{
			name: "Success/SubmittedOutsideATrace",
		},
		{
			name: "Success/UnreadableTraceContext",

			traceparent: new("not-a-traceparent"),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := otel.GetTracerProvider()

			otel.SetTracerProvider(provider)
			t.Cleanup(func() {
				otel.SetTracerProvider(previous)
				require.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context())))
			})

			check := core.NewGenerationCheck(
				coremocks.NewMockGenerationCheckDaoBeginStart(t),
				coremocks.NewMockGenerationCheckDaoReleaseStart(t),
				coremocks.NewMockGenerationCheckDaoRecordProviderCall(t),
				coremocks.NewMockGenerationCheckDaoSettle(t),
				coremocks.NewMockGenerationCheckDaoRequeue(t),
				coremocks.NewMockGenerationCheckDaoAttemptInsert(t),
				coremocks.NewMockGenerationCheckDaoUsageInsert(t),
				coremocks.NewMockGenerationCheckDaoGet(t),
				connectors.Registry{testProvider: connectorsmocks.NewMockAsync(t)},
				transactiontest.NewTransactor(),
				coremocks.NewMockGenerationCheckLogger(t),
				config.Generations{Retention: time.Hour},
			)

			generation := settledGeneration(dao.GenerationStatusSucceeded)
			generation.Traceparent = testCase.traceparent

			_, err := check.Exec(
				trace.ContextWithSpanContext(t.Context(), testCaller), &core.GenerationCheckRequest{Generation: generation},
			)
			require.NoError(t, err)

			spans := recorder.Ended()
			require.Len(t, spans, 1)
			require.Equal(t, "core.GenerationCheck", spans[0].Name())

			if testCase.expectLink == nil {
				require.Empty(t, spans[0].Links())

				return
			}

			require.Len(t, spans[0].Links(), 1)
			require.Equal(t, *testCase.expectLink, spans[0].Links()[0].SpanContext.TraceID().String())
			require.True(t, spans[0].Links()[0].SpanContext.IsRemote())
		})
	}
}
