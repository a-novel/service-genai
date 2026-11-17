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

	// settle describes the settle a case expects, and its error; the runner fills in the id and
	// retention.
	type settle struct {
		callID  *string
		status  dao.GenerationStatus
		reason  *string
		failure *dao.GenerationFailure
		// outcomeUnknown marks a call the provider may still run, untracked: the slot stays held.
		outcomeUnknown bool
		err            error
	}

	// startMock sends the generation's call, as the submit that recorded its intent does. The submit
	// began recording it intentAge before the check. The provider answers the send with call or err;
	// with neither, nothing is sent.
	type startMock struct {
		intentAge time.Duration
		call      *connectors.Call
		err       error
		// recordErrs fail the successive writes of the call id. The write after the last one lands,
		// unless the last one finds the generation moved on, which ends the writes.
		recordErrs []error
		// cancelledInFlight has the owner cancel while the start is in flight, so the recorded call
		// carries the request.
		cancelledInFlight bool
	}

	// observeMock is the provider's answer to the check's read of the call, or to its cancel.
	type observeMock struct {
		call *connectors.Call
		err  error
		// stop asks the provider to cancel instead of reading the call.
		stop bool
		// stopped is the provider's answer to a cancel sent after the read found the call stuck.
		stopped *connectors.Call
	}

	// attemptMock is the provider answer the attempt records.
	type attemptMock struct {
		response json.RawMessage
		err      error
	}

	// usageMock records what the observed call consumed.
	type usageMock struct {
		err error
	}

	// rereadMock is what the re-read finds, a settled generation unless stated.
	type rereadMock struct {
		generation *dao.Generation
		err        error
	}

	testCases := []struct {
		name string

		generation *dao.Generation
		start      *startMock
		observe    *observeMock
		// callerGone runs the check for a caller whose call already ended.
		callerGone bool

		expectStopOrphan bool
		expectSettle     *settle
		// expectNotRecorded settles the generation failed without the answer its settle could not record.
		expectNotRecorded *settle
		expectAttempt     *attemptMock
		expectUsage       *usageMock
		expectReread      *rereadMock

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
			start:      &startMock{call: providerCall(connectors.CallRunning)},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The connector creates a streamed generation's call in streaming mode.
			name: "Success/StreamedStart",

			generation: streamed(startingGeneration(0)),
			start:      &startMock{call: providerCall(connectors.CallRunning)},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The owner cancelled while the start was in flight: the recorded call is stopped at once,
			// rather than running until the next check.
			name: "Success/CancelledDuringStart",

			generation: startingGeneration(0),
			start:      &startMock{call: providerCall(connectors.CallRunning), cancelledInFlight: true},
			observe:    &observeMock{call: providerCall(connectors.CallCancelled), stop: true},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// A streamed start records its call while it holds; the cancel is applied once the hold ends.
			name: "Success/CancelledDuringStreamedStart",

			generation: streamed(startingGeneration(0)),
			start:      &startMock{call: providerCall(connectors.CallRunning), cancelledInFlight: true},
			observe:    &observeMock{call: providerCall(connectors.CallCancelled), stop: true},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// The stop's settle failed: the call stays recorded with its cancel, for the next check.
			name: "Error/CancelledDuringStartSettle",

			generation: startingGeneration(0),
			start:      &startMock{call: providerCall(connectors.CallRunning), cancelledInFlight: true},
			observe:    &observeMock{call: providerCall(connectors.CallCancelled), stop: true},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
				err: errFoo,
			},
			expectErr: errFoo,
		},
		{
			// The provider turned the call away before accepting it, so it ran nothing. Nothing sends it
			// again: the caller reads the class, and resends.
			name: "Success/StartRateLimited",

			generation: startingGeneration(0),
			start:      &startMock{err: connectors.ErrRetryable},

			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &startFailedMessage, failure: notStarted,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// Nothing ran. The caller built the request, so it reads the provider's reason to fix it.
			name: "Success/StartRejected",

			generation: startingGeneration(0),
			start:      &startMock{err: &connectors.RejectionError{Response: rejectionBody, Err: errFoo}},

			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &rejectedMessage, failure: rejected,
			},
			expectAttempt: &attemptMock{response: rejectionBody},
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// Recording the rejection failed: the start intent stays for a later check to resolve.
			name: "Error/StartRejectedSettle",

			generation: startingGeneration(0),
			start:      &startMock{err: &connectors.RejectionError{Response: rejectionBody, Err: errFoo}},

			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &rejectedMessage, failure: rejected,
				err: errFoo,
			},
			expectErr: errFoo,
		},
		{
			name: "Success/StartFailed",

			generation: startingGeneration(0),
			start:      &startMock{err: errFoo},

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
			start:      &startMock{err: connectors.ErrStartAmbiguous},

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
			start:      &startMock{intentAge: 30 * time.Second},

			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &startFailedMessage, failure: notStarted,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The generation settled while the start was in flight: the accepted call belongs to nothing.
			name: "Success/OrphanStopped",

			generation: startingGeneration(0),
			start:      &startMock{call: providerCall(connectors.CallRunning), recordErrs: []error{dao.ErrGenerationChanged}},

			expectStopOrphan: true,
			expectReread:     &rereadMock{},
			expectStatus:     dao.GenerationStatusSucceeded,
		},
		{
			// A deployment dropped the provider's connector: the call was never sent.
			name: "Success/ProviderUnavailable",

			generation: withProvider(startingGeneration(0), "gone"),
			start:      &startMock{},

			expectSettle: &settle{
				status: dao.GenerationStatusFailed, reason: &unavailableMessage, failure: notStarted,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			// The call runs on out of this service's reach and may still be paid for: the slot stays held.
			name: "Success/ProviderUnavailableWhileRunning",

			generation: withProvider(runningGeneration(), "gone"),

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &unavailableMessage,
				failure: unknown, outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
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
			observe:    &observeMock{call: providerCall(connectors.CallRunning)},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			name: "Success/Succeeded",

			generation: runningGeneration(),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded)},

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A cancelled call is not a free one: its answer is recorded.
			name: "Success/Cancelled",

			generation: withCancel(runningGeneration()),
			observe:    &observeMock{call: providerCall(connectors.CallCancelled)},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusCancelled,
		},
		{
			// Another check stopped the call on its owner's request after this one read the generation.
			name: "Success/CancelledMeanwhile",

			generation: runningGeneration(),
			observe:    &observeMock{call: providerCall(connectors.CallCancelled)},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusCancelled, reason: &cancelledReason,
			},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			// The re-read carries another start: the settle stays fenced on the start and the call this
			// check observed, and takes only the cancel request from the re-read.
			expectReread: &rereadMock{generation: withCancel(startedAgo(runningGeneration(), time.Hour))},
			expectStatus: dao.GenerationStatusCancelled,
		},
		{
			name: "Success/ProviderFailed",

			generation: runningGeneration(),
			observe:    &observeMock{call: failedCall()},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &callFailedMessage,
				failure: providerFailed,
			},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// A check that settles runs to the end on its own: cut by the caller's deadline, it would
			// leave the answer for the next one to read again.
			name: "Success/CallerGone",

			generation: runningGeneration(),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded)},
			callerGone: true,

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// Past the run ceiling, the call is stopped and the generation fails with what it consumed.
			name: "Success/Overdue",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    &observeMock{call: providerCall(connectors.CallCancelled), stop: true},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &overdueMessage,
				failure: providerFailed,
			},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// The call finished before the stop reached it: its result stands.
			name: "Success/OverdueFinishedMeanwhile",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded), stop: true},

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A provider that cannot be reached, a revoked key included, must not hold the slot forever.
			// The call may still run, so the generation keeps its slot a while, as for an unknown start.
			name: "Success/OverdueUnreachable",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    &observeMock{err: connectors.ErrRetryable, stop: true},

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
			observe:    &observeMock{err: connectors.ErrRetryable, stop: true},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A stop the provider never applies ends the generation too, which keeps its slot a while.
			name: "Success/OverdueStopNotApplied",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    &observeMock{call: providerCall(connectors.CallRunning), stop: true},

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
			observe:    &observeMock{call: providerCall(connectors.CallQueued), stopped: providerCall(connectors.CallQueued)},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The window counts from the stop at 5 minutes, not from the run ceiling: a stuck streamed
			// call the stop never reaches is given up on around 15 minutes.
			name: "Success/StreamedStuckQueuedGivenUp",

			generation: streamed(startedAgo(runningGeneration(), 20*time.Minute)),
			observe:    &observeMock{call: providerCall(connectors.CallQueued), stopped: providerCall(connectors.CallQueued)},

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
			observe:    &observeMock{err: connectors.ErrRetryable},

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
			observe:    &observeMock{err: connectors.ErrRetryable},

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
			observe:    &observeMock{err: connectors.ErrRetryable},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A streamed creation stream left early can leave the call queued for good: past its
			// ceiling, it is stopped like an overdue one.
			name: "Success/StreamedStuckQueued",

			generation: streamed(startedAgo(runningGeneration(), 10*time.Minute)),
			observe:    &observeMock{call: providerCall(connectors.CallQueued), stopped: providerCall(connectors.CallCancelled)},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &overdueMessage,
				failure: providerFailed,
			},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{},
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			name: "Success/StreamedQueuedWithinCeiling",

			generation: streamed(startedAgo(runningGeneration(), time.Minute)),
			observe:    &observeMock{call: providerCall(connectors.CallQueued)},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// Only a streamed creation risks the early-disconnect bug; a polled call may queue a while.
			name: "Success/PolledQueuedLong",

			generation: startedAgo(runningGeneration(), 10*time.Minute),
			observe:    &observeMock{call: providerCall(connectors.CallQueued)},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// The next check asks again; a provider hiccup is not the generation's failure.
			name: "Success/TransientObservationFailure",

			generation: runningGeneration(),
			observe:    &observeMock{err: connectors.ErrRetryable},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// Gone past the provider's retention window. Starting again would pay twice.
			name: "Success/CallLost",

			generation: runningGeneration(),
			observe:    &observeMock{err: errFoo},

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
			observe:    &observeMock{err: fmt.Errorf("%w: %w", connectors.ErrResponseTooLarge, errFoo)},

			expectSettle: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &tooLargeMessage,
				failure: unknown, outcomeUnknown: true,
			},
			expectStatus: dao.GenerationStatusFailed,
		},
		{
			name: "Success/SettledByAnotherCheck",

			generation: runningGeneration(),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded)},

			expectSettle: &settle{callID: &callID, status: dao.GenerationStatusSucceeded, err: dao.ErrGenerationChanged},
			expectReread: &rereadMock{},
			expectStatus: dao.GenerationStatusSucceeded,
		},
		{
			// The first write got no answer, as on a migration's lock; a later one lands, and the call
			// keeps running.
			name: "Success/RecordRetried",

			generation: startingGeneration(0),
			start:      &startMock{call: providerCall(connectors.CallRunning), recordErrs: []error{errFoo}},

			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A write failed but landed, so the next finds the generation moved on: it carries the call,
			// which keeps running.
			name: "Success/RecordLanded",

			generation: startingGeneration(0),
			start: &startMock{
				call: providerCall(connectors.CallRunning), recordErrs: []error{errFoo, dao.ErrGenerationChanged},
			},

			expectReread: &rereadMock{generation: runningGeneration()},
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A streamed start keeps holding the call its late write recorded: leaving the stream while
			// the call is queued can leave it queued for good.
			name: "Success/StreamedRecordLanded",

			generation: streamed(startingGeneration(0)),
			start: &startMock{
				call: providerCall(connectors.CallQueued), recordErrs: []error{errFoo, dao.ErrGenerationChanged},
			},

			expectReread: &rereadMock{generation: runningGeneration()},
			expectStatus: dao.GenerationStatusRunning,
		},
		{
			// A generation that cannot be read may carry the call: it is left running, and the next
			// check finds out.
			name: "Error/RecordUnreadable",

			generation: startingGeneration(0),
			start: &startMock{
				call: providerCall(connectors.CallRunning), recordErrs: []error{errFoo, dao.ErrGenerationChanged},
			},

			expectReread: &rereadMock{err: errFoo},
			expectErr:    errFoo,
		},
		{
			// The writes failed, and another check settled the generation meanwhile: nothing tracks the
			// accepted call, so it is stopped. That check's outcome is the answer.
			name: "Success/RecordLost",

			generation: startingGeneration(0),
			start: &startMock{
				call: providerCall(connectors.CallRunning), recordErrs: []error{errFoo, dao.ErrGenerationChanged},
			},

			expectStopOrphan: true,
			expectReread:     &rereadMock{generation: withoutCall(settledGeneration(dao.GenerationStatusFailed))},
			expectStatus:     dao.GenerationStatusFailed,
		},
		{
			// Recorded while the hold goes on: a write that never landed stops the call there too.
			name: "Success/StreamedRecordLost",

			generation: streamed(startingGeneration(0)),
			start: &startMock{
				call: providerCall(connectors.CallQueued), recordErrs: []error{errFoo, dao.ErrGenerationChanged},
			},

			expectStopOrphan: true,
			expectReread:     &rereadMock{generation: withoutCall(settledGeneration(dao.GenerationStatusFailed))},
			expectStatus:     dao.GenerationStatusFailed,
		},
		{
			name: "Error/Settle",

			generation: runningGeneration(),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded)},

			expectSettle: &settle{callID: &callID, status: dao.GenerationStatusSucceeded, err: errFoo},
			expectErr:    errFoo,
		},
		{
			// The stop of an orphaned call recorded this attempt's usage before its call id landed: it is
			// counted once.
			name: "Success/UsageAlreadyRecorded",

			generation: runningGeneration(),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded)},

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{err: dao.ErrGenerationUsageExists},
			expectStatus:  dao.GenerationStatusSucceeded,
		},
		{
			// A billed call nothing counts would leak past every cap: the settle waits for its usage.
			name: "Error/Usage",

			generation: runningGeneration(),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded)},

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: &attemptMock{response: testResponse},
			expectUsage:   &usageMock{err: errFoo},
			expectErr:     errFoo,
		},
		{
			// An answer the database refuses cannot hold the generation running for as long as the
			// provider keeps it. It fails without the answer at the age a failing stop is given up on.
			name: "Success/AnswerNotRecorded",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded), stop: true},

			expectSettle: &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectNotRecorded: &settle{
				callID: &callID, status: dao.GenerationStatusFailed, reason: &notRecordedMessage,
				failure: providerFailed,
			},
			expectAttempt: &attemptMock{response: testResponse, err: errors.Join(errFoo, dao.ErrRefused)},
			expectStatus:  dao.GenerationStatusFailed,
		},
		{
			// A failure the database may not repeat, such as a lock wait, keeps the paid answer for the
			// next check, however long the call ran.
			name: "Error/AnswerNotRecordedYet",

			generation: startedAgo(runningGeneration(), 3*time.Hour),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded), stop: true},

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: &attemptMock{response: testResponse, err: errFoo},
			expectErr:     errFoo,
		},
		{
			// The settle and its answer commit together, or not at all.
			name: "Error/Attempt",

			generation: runningGeneration(),
			observe:    &observeMock{call: providerCall(connectors.CallSucceeded)},

			expectSettle:  &settle{callID: &callID, status: dao.GenerationStatusSucceeded},
			expectAttempt: &attemptMock{response: testResponse, err: errFoo},
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

			start := lo.FromPtr(testCase.start)

			if start.call != nil || start.err != nil {
				// The payload as the caller built it: the connector writes its own envelope.
				connector.EXPECT().
					Start(mock.Anything, mock.MatchedBy(func(request *connectors.AsyncStartRequest) bool {
						return bytes.Equal(request.Payload, testPayload) && request.Stream == generation.Streamed
					})).
					RunAndReturn(func(_ context.Context, request *connectors.AsyncStartRequest) (*connectors.Call, error) {
						// A streamed start names its call while it holds, before it returns.
						if start.call != nil && generation.Streamed {
							err := request.Accepted(start.call)
							if err != nil {
								return nil, err
							}
						}

						return start.call, start.err
					})
			}

			if start.call != nil {
				recordRequest := &dao.GenerationRecordProviderCallRequest{
					ID: generation.ID, ProviderCallID: testCallID,
				}

				// The intent, now running its call.
				recorded := runningGeneration()
				recorded.StartRequestedAt = generation.StartRequestedAt

				if start.cancelledInFlight {
					recorded = withCancel(recorded)
				}

				for _, recordErr := range start.recordErrs {
					record.EXPECT().Exec(mock.Anything, recordRequest).Return(nil, recordErr).Once()
				}

				if !errors.Is(lo.LastOrEmpty(start.recordErrs), dao.ErrGenerationChanged) {
					record.EXPECT().Exec(mock.Anything, recordRequest).Return(recorded, nil).Once()
				}
			}

			if testCase.expectStopOrphan {
				orphan := providerCall(connectors.CallCancelled)

				connector.EXPECT().Cancel(mock.Anything, testCallID).Return(orphan, nil)
				// The orphan ran on the provider's side: what it consumed still counts.
				usage.EXPECT().Exec(mock.Anything, usageOf(generation, orphan)).Return(&dao.GenerationUsage{}, nil)
			}

			if observe := testCase.observe; observe != nil {
				if generation.CancelRequestedAt != nil || observe.stop {
					connector.EXPECT().Cancel(mock.Anything, testCallID).Return(observe.call, observe.err)
				} else {
					// The read must run on a live context, whatever became of the caller's.
					connector.EXPECT().Get(mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil }), testCallID).
						Return(observe.call, observe.err)
				}

				if observe.stopped != nil {
					connector.EXPECT().Cancel(mock.Anything, testCallID).Return(observe.stopped, nil)
				}
			}

			for _, expected := range []*settle{testCase.expectSettle, testCase.expectNotRecorded} {
				if expected == nil {
					continue
				}

				settleRequest := &dao.GenerationSettleRequest{
					ID: generation.ID,
					// A settle is fenced on the start it read.
					StartRequestedAt: generation.StartRequestedAt,
					ProviderCallID:   expected.callID,
					Status:           expected.status,
					Error:            expected.reason,
					Failure:          expected.failure,
					Retention:        retention,
					OutcomeUnknown:   expected.outcomeUnknown,
				}

				settleDao.EXPECT().
					Exec(mock.Anything, settleRequest).
					Return(settledGeneration(expected.status), expected.err)
			}

			if expected := testCase.expectAttempt; expected != nil {
				attempt.EXPECT().
					Exec(mock.Anything, &dao.GenerationAttemptInsertRequest{
						GenerationID: generation.ID, Response: expected.response,
					}).
					Return(&dao.GenerationAttempt{}, expected.err)
			}

			if expected := testCase.expectUsage; expected != nil {
				observe := lo.FromPtr(testCase.observe)

				usage.EXPECT().
					Exec(mock.Anything, usageOf(generation, lo.CoalesceOrEmpty(observe.stopped, observe.call))).
					Return(&dao.GenerationUsage{}, expected.err)
			}

			if expected := testCase.expectReread; expected != nil {
				reread := lo.CoalesceOrEmpty(expected.generation, settledGeneration(dao.GenerationStatusSucceeded))
				if expected.err != nil {
					reread = nil
				}

				get.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{ID: generation.ID, OwnerID: generation.OwnerID}).
					Return(reread, expected.err)
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
			if testCase.start != nil {
				request.Start = &core.GenerationCheckStart{Payload: testPayload, IntentAt: time.Now().Add(-start.intentAge)}
			}

			result, err := check.Exec(ctx, request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.expectStatus, result.Status)
			}
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
		StartedAt:         generation.StartRequestedAt,
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
