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

	notStarted := new(dao.GenerationFailureNotStarted)
	rejected := new(dao.GenerationFailureRejected)
	providerFailed := new(dao.GenerationFailureProviderFailed)
	unknown := new(dao.GenerationFailureOutcomeUnknown)

	// settle describes the settle a case expects; the runner fills in the id and retention.
	type settle struct {
		callID  *string
		status  dao.GenerationStatus
		reason  *string
		failure *dao.GenerationFailure
		// outcomeUnknown marks a start the provider may have accepted: the slot stays held.
		outcomeUnknown bool
	}

	testCases := []struct {
		name string

		generation *dao.Generation

		// start sends the generation's call, as the submit that recorded its intent does. The submit
		// began recording it intentAge before the check.
		start     bool
		intentAge time.Duration
		started   *connectors.Call
		startErr  error
		// recordErrs fail the successive writes of the call id. The write after the last one lands,
		// unless the last one finds the generation moved on, which ends the writes.
		recordErrs []error
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

		expectSend       bool
		expectStopOrphan bool
		expectSettle     *settle
		// expectNotRecorded settles the generation failed without the answer its settle could not record.
		expectNotRecorded *settle
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
			// The submit sends the call it recorded, and its first write records the call id.
			name: "Success/Start",

			generation: startingGeneration(0),
			start:      true,
			started:    providerCall(connectors.CallRunning),

			expectSend:   true,
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The connector creates a streamed generation's call in streaming mode.
			name: "Success/StreamedStart",

			generation: streamed(startingGeneration(0)),
			start:      true,
			started:    providerCall(connectors.CallRunning),

			expectSend:   true,
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The owner cancelled while the start was in flight: the recorded call is stopped at once,
			// rather than running until the next check.
			name: "Success/CancelledDuringStart",

			generation:        startingGeneration(0),
			start:             true,
			started:           providerCall(connectors.CallRunning),
			cancelledInFlight: true,
			expectStop:        true,
			observe:           providerCall(connectors.CallCancelled),

			expectSend: true,
			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// A streamed start records its call while it holds; the cancel is applied once the hold ends.
			name: "Success/CancelledDuringStreamedStart",

			generation:        streamed(startingGeneration(0)),
			start:             true,
			started:           providerCall(connectors.CallRunning),
			cancelledInFlight: true,
			expectStop:        true,
			observe:           providerCall(connectors.CallCancelled),

			expectSend: true,
			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// The stop's settle failed: the call stays recorded with its cancel, for the next check.
			name: "Error/CancelledDuringStartSettle",

			generation:        startingGeneration(0),
			start:             true,
			started:           providerCall(connectors.CallRunning),
			cancelledInFlight: true,
			expectStop:        true,
			observe:           providerCall(connectors.CallCancelled),
			settleErr:         errFoo,

			expectSend: true,
			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectErr: errFoo,
		},
		{
			// The provider turned the call away before accepting it, so it ran nothing. Nothing sends it
			// again: the caller reads the class, and resends.
			name: "Success/StartRateLimited",

			generation: startingGeneration(0),
			start:      true,
			startErr:   connectors.ErrRetryable,

			expectSend: true,
			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &startFailedMessage, failure: notStarted,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// Nothing ran. The caller built the request, so it reads the provider's reason to fix it.
			name: "Success/StartRejected",

			generation: startingGeneration(0),
			start:      true,
			startErr:   &connectors.RejectionError{Response: rejectionBody, Err: errFoo},

			expectSend: true,
			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &rejectedMessage, failure: rejected,
			},
			expectAttempt: rejectionBody,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// Recording the rejection failed: the start intent stays for a later check to resolve.
			name: "Error/StartRejectedSettle",

			generation: startingGeneration(0),
			start:      true,
			startErr:   &connectors.RejectionError{Response: rejectionBody, Err: errFoo},
			settleErr:  errFoo,

			expectSend: true,
			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &rejectedMessage, failure: rejected,
			},
			expectErr: errFoo,
		},
		{
			name: "Success/StartFailed",

			generation: startingGeneration(0),
			start:      true,
			startErr:   errFoo,

			expectSend: true,
			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &startFailedMessage, failure: notStarted,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The call may have been accepted. Sending another could pay twice, so the generation ends,
			// and keeps its slot a while.
			name: "Success/StartAmbiguous",

			generation: startingGeneration(0),
			start:      true,
			startErr:   connectors.ErrStartAmbiguous,

			expectSend: true,
			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &unknownMessage, failure: unknown,
				outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The process froze since the intent: another check may have settled it already, and a call
			// sent now would run unread.
			name: "Success/StartTooOld",

			generation: startingGeneration(0),
			start:      true,
			intentAge:  30 * time.Second,

			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &startFailedMessage, failure: notStarted,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The generation settled while the start was in flight: the accepted call belongs to nothing.
			name: "Success/OrphanStopped",

			generation: startingGeneration(0),
			start:      true,
			started:    providerCall(connectors.CallRunning),
			recordErrs: []error{dao.ErrGenerationChanged},

			expectSend:       true,
			expectStopOrphan: true,
			expectReread:     true,
			expectStatus:     dao.GenerationStatusSucceeded,
		},
		{
			// A deployment dropped the provider's connector: the call was never sent.
			name: "Success/ProviderUnavailable",

			generation: withProvider(startingGeneration(0), "gone"),
			start:      true,

			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &unavailableMessage, failure: notStarted,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The call runs on out of this service's reach: it may still be paid for.
			name: "Success/ProviderUnavailableWhileRunning",

			generation: withProvider(runningGeneration(), "gone"),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &unavailableMessage,
				failure: unknown,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// An earlier release left the generation between two starts, its payload out of reach.
			name: "Success/NoStartIntent",

			generation: pendingGeneration(),

			expectSettle: &settle{status: dao.GenerationStatusFailed, reason: &startFailedMessage, failure: notStarted},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/CancelledBeforeStart",

			generation: withCancel(pendingGeneration()),

			expectSettle: &settle{status: dao.GenerationStatusCancelled, reason: &cancelledReason},
			expectStatus: dao.GenerationStatusCancelled,
		},
		{
			// The submit may still be sending this start, or writing its call id: nothing happens yet.
			name: "Success/StartInFlight",

			generation: startingGeneration(10 * time.Second),

			expectStatus: dao.GenerationStatusPending,
		},
		{
			// Past the record's patience, a write may still be landing: the check leaves it room.
			name: "Success/StartRecordStillPatient",

			generation: startingGeneration(2*time.Minute + 30*time.Second),

			expectStatus: dao.GenerationStatusPending,
		},
		{
			name: "Success/StartOutcomeUnknown",

			generation: startingGeneration(3 * time.Minute),

			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &unknownMessage, failure: unknown,
				outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/StillRunning",

			generation: runningGeneration(),
			observe:    providerCall(connectors.CallRunning),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			name: "Success/Succeeded",

			generation: runningGeneration(),
			observe:    providerCall(connectors.CallSucceeded),

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A cancelled call is not a free one: its answer is recorded.
			name: "Success/Cancelled",

			generation: withCancel(runningGeneration()),
			observe:    providerCall(connectors.CallCancelled),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// Another check stopped the call on its owner's request after this one read the generation.
			name: "Success/CancelledMeanwhile",

			generation:   runningGeneration(),
			observe:      providerCall(connectors.CallCancelled),
			expectReread: true,
			// The re-read carries another start: the settle stays fenced on the start and the call this
			// check observed, and takes only the cancel request from the re-read.
			rereadAs: withCancel(startedAgo(runningGeneration(), time.Hour)),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			name: "Success/ProviderFailed",

			generation: runningGeneration(),
			observe:    failedCall(false),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
				failure: providerFailed,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// A failed call settles on its first outcome, even one the provider calls retryable: the
			// caller resends it.
			name: "Success/RetryableFailure",

			generation: runningGeneration(),
			observe:    failedCall(true),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
				failure: providerFailed,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// A check that settles runs to the end on its own: cut by the caller's deadline, it would
			// leave the answer for the next one to read again.
			name: "Success/CallerGone",

			generation: runningGeneration(),
			observe:    providerCall(connectors.CallSucceeded),
			callerGone: true,

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// Past the run ceiling, the call is stopped and the generation fails with what it consumed.
			name: "Success/Overdue",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    providerCall(connectors.CallCancelled),
			expectStop: true,

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &overdueMessage,
				failure: providerFailed,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// The call finished before the stop reached it: its result stands.
			name: "Success/OverdueFinishedMeanwhile",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    providerCall(connectors.CallSucceeded),
			expectStop: true,

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A provider that cannot be reached, a revoked key included, must not hold the slot forever.
			// The call may still run, so the generation keeps its slot a while, as for an unknown start.
			name: "Success/OverdueUnreachable",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observeErr: connectors.ErrRetryable,
			expectStop: true,

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				failure: unknown, outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// Just past the ceiling, a stop that fails is asked again before the call is given up on.
			name: "Success/OverdueStopRetried",

			generation: startedAgo(runningGeneration(), runCeilingForTest+time.Minute),
			observeErr: connectors.ErrRetryable,
			expectStop: true,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A stop the provider never applies ends the generation too, which keeps its slot a while.
			name: "Success/OverdueStopNotApplied",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    providerCall(connectors.CallRunning),
			expectStop: true,

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				failure: unknown, outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// A stuck streamed call the stop has not reached yet is asked again by the next check.
			name: "Success/StreamedStuckQueuedStopNotApplied",

			generation: streamed(startedAgo(runningGeneration(), 10*time.Minute)),
			observe:    providerCall(connectors.CallQueued),
			stopped:    providerCall(connectors.CallQueued),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The window counts from the stop at 5 minutes, not from the run ceiling: a stuck streamed
			// call the stop never reaches is given up on around 15 minutes.
			name: "Success/StreamedStuckQueuedGivenUp",

			generation: streamed(startedAgo(runningGeneration(), 20*time.Minute)),
			observe:    providerCall(connectors.CallQueued),
			stopped:    providerCall(connectors.CallQueued),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				failure: unknown, outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// An owner's cancel that keeps failing is given up on 10 minutes after the request, far from
			// the run ceiling.
			name: "Success/CancelGivenUp",

			generation: cancelledAgo(startedAgo(runningGeneration(), 30*time.Minute), 20*time.Minute),
			observeErr: connectors.ErrRetryable,

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				failure: unknown, outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The ceiling bounds a call its owner cancelled too: a stop that keeps failing ends there.
			name: "Success/CancelledPastCeilingUnreachable",

			generation: withCancel(startedAgo(runningGeneration(), 3*time.Hour)),
			observeErr: connectors.ErrRetryable,

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &notStoppedMessage,
				failure: unknown, outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// Within the ceiling, a stop that fails is asked again by the next check.
			name: "Success/CancelRetried",

			generation: withCancel(runningGeneration()),
			observeErr: connectors.ErrRetryable,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A streamed creation stream left early can leave the call queued for good: past its
			// ceiling, it is stopped like an overdue one.
			name: "Success/StreamedStuckQueued",

			generation: streamed(startedAgo(runningGeneration(), 10*time.Minute)),
			observe:    providerCall(connectors.CallQueued),
			stopped:    providerCall(connectors.CallCancelled),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &overdueMessage,
				failure: providerFailed,
			},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			name: "Success/StreamedQueuedWithinCeiling",

			generation: streamed(startedAgo(runningGeneration(), time.Minute)),
			observe:    providerCall(connectors.CallQueued),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// Only a streamed creation risks the early-disconnect bug; a polled call may queue a while.
			name: "Success/PolledQueuedLong",

			generation: startedAgo(runningGeneration(), 10*time.Minute),
			observe:    providerCall(connectors.CallQueued),

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The next check asks again; a provider hiccup is not the generation's failure.
			name: "Success/TransientObservationFailure",

			generation: runningGeneration(),
			observeErr: connectors.ErrRetryable,

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// Gone past the provider's retention window. Starting again would pay twice.
			name: "Success/CallLost",

			generation: runningGeneration(),
			observeErr: errFoo,

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &lostMessage,
				failure: providerFailed,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// No read can ever get the call's answer, nor tell whether it still runs: the slot stays held.
			name: "Success/ResponseTooLarge",

			generation: runningGeneration(),
			observeErr: fmt.Errorf("%w: %w", connectors.ErrResponseTooLarge, errFoo),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &tooLargeMessage,
				failure: unknown, outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/SettledByAnotherCheck",

			generation: runningGeneration(),
			observe:    providerCall(connectors.CallSucceeded),
			settleErr:  dao.ErrGenerationChanged,

			expectSettle: &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectReread: true,
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			// The first write got no answer, as on a migration's lock; a later one lands, and the call
			// keeps running.
			name: "Success/RecordRetried",

			generation: startingGeneration(0),
			start:      true,
			started:    providerCall(connectors.CallRunning),
			recordErrs: []error{errFoo},

			expectSend:   true,
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A write failed but landed, so the next finds the generation moved on: it carries the call,
			// which keeps running.
			name: "Success/RecordLanded",

			generation: startingGeneration(0),
			start:      true,
			started:    providerCall(connectors.CallRunning),
			recordErrs: []error{errFoo, dao.ErrGenerationChanged},

			expectSend:   true,
			expectReread: true,
			rereadAs:     runningGeneration(),
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A streamed start keeps holding the call its late write recorded: leaving the stream while
			// the call is queued can leave it queued for good.
			name: "Success/StreamedRecordLanded",

			generation: streamed(startingGeneration(0)),
			start:      true,
			started:    providerCall(connectors.CallQueued),
			recordErrs: []error{errFoo, dao.ErrGenerationChanged},

			expectSend:   true,
			expectReread: true,
			rereadAs:     runningGeneration(),
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A generation that cannot be read may carry the call: it is left running, and the next
			// check finds out.
			name: "Error/RecordUnreadable",

			generation: startingGeneration(0),
			start:      true,
			started:    providerCall(connectors.CallRunning),
			recordErrs: []error{errFoo, dao.ErrGenerationChanged},
			rereadErr:  errFoo,

			expectSend:   true,
			expectReread: true,
			expectErr:    errFoo,
		},
		{
			// The writes failed, and another check settled the generation meanwhile: nothing tracks the
			// accepted call, so it is stopped. That check's outcome is the answer.
			name: "Success/RecordLost",

			generation: startingGeneration(0),
			start:      true,
			started:    providerCall(connectors.CallRunning),
			recordErrs: []error{errFoo, dao.ErrGenerationChanged},

			expectSend:       true,
			expectReread:     true,
			rereadAs:         withoutCall(settledGeneration(dao.GenerationStatusFailed)),
			expectStopOrphan: true,
			expectStatus:     dao.GenerationStatusFailed,
		},
		{
			// Recorded while the hold goes on: a write that never landed stops the call there too.
			name: "Success/StreamedRecordLost",

			generation: streamed(startingGeneration(0)),
			start:      true,
			started:    providerCall(connectors.CallQueued),
			recordErrs: []error{errFoo, dao.ErrGenerationChanged},

			expectSend:       true,
			expectReread:     true,
			rereadAs:         withoutCall(settledGeneration(dao.GenerationStatusFailed)),
			expectStopOrphan: true,
			expectStatus:     dao.GenerationStatusFailed,
		},
		{
			name: "Error/Settle",

			generation: runningGeneration(),
			observe:    providerCall(connectors.CallSucceeded),
			settleErr:  errFoo,

			expectSettle: &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectErr:    errFoo,
		},
		{
			// The stop of an orphaned call recorded this attempt's usage before its call id landed: it is
			// counted once.
			name: "Success/UsageAlreadyRecorded",

			generation: runningGeneration(),
			observe:    providerCall(connectors.CallSucceeded),
			usageErr:   dao.ErrGenerationUsageExists,

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A billed call nothing counts would leak past every cap: the settle waits for its usage.
			name: "Error/Usage",

			generation: runningGeneration(),
			observe:    providerCall(connectors.CallSucceeded),
			usageErr:   errFoo,

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectUsage:   true,
			expectErr:     errFoo,
		},
		{
			// An answer the database refuses cannot hold the generation running for as long as the
			// provider keeps it. It fails without the answer at the age a failing stop is given up on.
			name: "Success/AnswerNotRecorded",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    providerCall(connectors.CallSucceeded),
			expectStop: true,
			attemptErr: errors.Join(errFoo, dao.ErrRefused),

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectNotRecorded: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &notRecordedMessage,
				failure: providerFailed,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// A failure the database may not repeat, such as a lock wait, keeps the paid answer for the
			// next check, however long the call ran.
			name: "Error/AnswerNotRecordedYet",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    providerCall(connectors.CallSucceeded),
			expectStop: true,
			attemptErr: errFoo,

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectErr:     errFoo,
		},
		{
			// The settle and its answer commit together, or not at all.
			name: "Error/Attempt",

			generation: runningGeneration(),
			observe:    providerCall(connectors.CallSucceeded),
			attemptErr: errFoo,

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: testResponse,
			expectErr:     errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			generation := testCase.generation

			connector := connectorsmocks.NewMockAsync(t)
			record := coremocks.NewMockGenerationCheckDaoRecordProviderCall(t)
			settleDao := coremocks.NewMockGenerationCheckDaoSettle(t)
			attempt := coremocks.NewMockGenerationCheckDaoAttemptInsert(t)
			usage := coremocks.NewMockGenerationCheckDaoUsageInsert(t)
			get := coremocks.NewMockGenerationCheckDaoGet(t)

			if testCase.expectSend {
				// The payload as the caller built it: the connector writes its own envelope.
				connector.EXPECT().
					Start(mock.Anything, mock.MatchedBy(func(request *connectors.AsyncStartRequest) bool {
						return bytes.Equal(request.Payload, testPayload) && request.Stream == generation.Streamed
					})).
					RunAndReturn(func(_ context.Context, request *connectors.AsyncStartRequest) (*connectors.Call, error) {
						// A streamed start names its call while it holds, before it returns.
						if testCase.started != nil && generation.Streamed {
							err := request.Accepted(testCase.started)
							if err != nil {
								return nil, err
							}
						}

						return testCase.started, testCase.startErr
					})
			}

			if testCase.started != nil {
				recordRequest := &dao.GenerationRecordProviderCallRequest{
					ID: generation.ID, ProviderCallID: testCallID,
				}

				// The intent, now running its call.
				recorded := runningGeneration()
				recorded.StartRequestedAt = generation.StartRequestedAt

				if testCase.cancelledInFlight {
					recorded = withCancel(recorded)
				}

				for _, recordErr := range testCase.recordErrs {
					record.EXPECT().Exec(mock.Anything, recordRequest).Return(nil, recordErr).Once()
				}

				if !errors.Is(lo.LastOrEmpty(testCase.recordErrs), dao.ErrGenerationChanged) {
					record.EXPECT().Exec(mock.Anything, recordRequest).Return(recorded, nil).Once()
				}
			}

			if testCase.expectStopOrphan {
				orphan := providerCall(connectors.CallCancelled)

				connector.EXPECT().Cancel(mock.Anything, testCallID).Return(orphan, nil)
				// The orphan ran on the provider's side: what it consumed still counts.
				usage.EXPECT().Exec(mock.Anything, usageOf(generation, orphan)).Return(&dao.GenerationUsage{}, nil)
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

			settles := []struct {
				settle *settle
				err    error
			}{
				{settle: testCase.expectSettle, err: testCase.settleErr},
				{settle: testCase.expectNotRecorded},
			}

			for _, expected := range settles {
				if expected.settle == nil {
					continue
				}

				settleRequest := &dao.GenerationSettleRequest{
					ID: generation.ID,
					// A settle is fenced on the start it read.
					StartRequestedAt: generation.StartRequestedAt,
					ProviderCallID:   expected.settle.callID,
					Status:           expected.settle.status,
					Error:            expected.settle.reason,
					Failure:          expected.settle.failure,
					Retention:        retention,
					OutcomeUnknown:   expected.settle.outcomeUnknown,
				}

				settleDao.EXPECT().
					Exec(mock.Anything, settleRequest).
					Return(settledGeneration(expected.settle.status), expected.err)
			}

			if testCase.expectAttempt != nil {
				attempt.EXPECT().
					Exec(mock.Anything, &dao.GenerationAttemptInsertRequest{
						GenerationID: generation.ID, Response: testCase.expectAttempt,
					}).
					Return(&dao.GenerationAttempt{}, testCase.attemptErr)
			}

			if testCase.expectUsage {
				usage.EXPECT().
					Exec(mock.Anything, usageOf(generation, lo.CoalesceOrEmpty(testCase.stopped, testCase.observe))).
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
				record, settleDao, attempt, usage, get,
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

			request := &core.GenerationCheckRequest{Generation: generation}
			if testCase.start {
				request.Start = &core.GenerationCheckStart{
					Payload: testPayload, IntentAt: time.Now().Add(-testCase.intentAge),
				}
			}

			result, err := check.Exec(ctx, request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.expectStatus, result.Status)
			}

			mock.AssertExpectationsForObjects(t, connector, record, settleDao, attempt, usage, get)
		})
	}
}

// usageOf is the Usage Record of a generation's answered call. The test provider reports no start, so
// the call is timed from when it was sent.
func usageOf(generation *dao.Generation, call *connectors.Call) *dao.GenerationUsageInsertRequest {
	return &dao.GenerationUsageInsertRequest{
		GenerationID:      generation.ID,
		OwnerID:           generation.OwnerID,
		Purpose:           generation.Purpose,
		Provider:          generation.Provider,
		Model:             call.Model,
		Class:             call.Class,
		StartedAt:         *generation.StartRequestedAt,
		InputTokens:       call.Usage.InputTokens,
		CachedInputTokens: call.Usage.CachedInputTokens,
		OutputTokens:      call.Usage.OutputTokens,
		ReasoningTokens:   call.Usage.ReasoningTokens,
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
				coremocks.NewMockGenerationCheckDaoRecordProviderCall(t),
				coremocks.NewMockGenerationCheckDaoSettle(t),
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
