package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/transaction"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// GenerationCheckLogger records failures whose provider details must remain server-side.
type GenerationCheckLogger interface {
	Err(ctx context.Context, msg string, fields ...any)
}

// GenerationCheckDaoRecordProviderCall attaches the accepted provider operation.
type GenerationCheckDaoRecordProviderCall interface {
	Exec(ctx context.Context, request *dao.GenerationRecordProviderCallRequest) (*dao.Generation, error)
}

// GenerationCheckDaoSettle records a terminal outcome.
type GenerationCheckDaoSettle interface {
	Exec(ctx context.Context, request *dao.GenerationSettleRequest) (*dao.Generation, error)
}

// GenerationCheckDaoAttemptInsert records the provider's answer to one attempt.
type GenerationCheckDaoAttemptInsert interface {
	Exec(ctx context.Context, request *dao.GenerationAttemptInsertRequest) (*dao.GenerationAttempt, error)
}

// GenerationCheckDaoUsageInsert records what one attempt consumed.
type GenerationCheckDaoUsageInsert interface {
	Exec(ctx context.Context, request *dao.GenerationUsageInsertRequest) (*dao.GenerationUsage, error)
}

// GenerationCheckDaoGet re-reads a generation another check moved on.
type GenerationCheckDaoGet interface {
	Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
}

// GenerationCheckRequest carries the generation to check, as last read.
type GenerationCheckRequest struct {
	// Generation is the generation as the caller last read it.
	Generation *dao.Generation
	// Start sends the provider call of a generation whose start intent the caller just recorded. Only
	// the submit that recorded the intent holds it. Without it, a check leaves the start to that
	// submit, and settles the generation once its start can no longer be in flight.
	Start *GenerationCheckStart
}

// GenerationCheckStart is what a check needs to send a generation's provider call.
type GenerationCheckStart struct {
	// Payload is the provider's own request, held in memory: this service never stores it.
	Payload json.RawMessage
	// IntentAt is when the caller began recording the start intent, by this process's clock. The send
	// and the write of its call id are timed from it.
	IntentAt time.Time
}

// A GenerationCheck looks at one generation and applies whatever transition is due: send the call
// its submit holds, or ask the provider once and settle the generation when the call ended.
//
// A run writes its generation twice after the insert: the call id once the provider accepts the call,
// and the settle. A check that finds nothing to settle writes nothing. No process owns a generation:
// every write is fenced by what the check read, so concurrent checks are safe, and a check interrupted
// at any point is redone by the next.
type GenerationCheck struct {
	daoRecord   GenerationCheckDaoRecordProviderCall
	daoSettle   GenerationCheckDaoSettle
	daoAttempt  GenerationCheckDaoAttemptInsert
	daoUsage    GenerationCheckDaoUsageInsert
	daoGet      GenerationCheckDaoGet
	registry    connectors.Registry
	transactor  transaction.Transactor
	logger      GenerationCheckLogger
	generations config.Generations
}

// NewGenerationCheck returns a new GenerationCheck service.
func NewGenerationCheck(
	daoRecord GenerationCheckDaoRecordProviderCall,
	daoSettle GenerationCheckDaoSettle,
	daoAttempt GenerationCheckDaoAttemptInsert,
	daoUsage GenerationCheckDaoUsageInsert,
	daoGet GenerationCheckDaoGet,
	registry connectors.Registry,
	transactor transaction.Transactor,
	logger GenerationCheckLogger,
	generations config.Generations,
) *GenerationCheck {
	return &GenerationCheck{
		daoRecord:   daoRecord,
		daoSettle:   daoSettle,
		daoAttempt:  daoAttempt,
		daoUsage:    daoUsage,
		daoGet:      daoGet,
		registry:    registry,
		transactor:  transactor,
		logger:      logger,
		generations: generations,
	}
}

// Exec returns the generation as the check left it.
func (service *GenerationCheck) Exec(
	ctx context.Context, request *GenerationCheckRequest,
) (*dao.Generation, error) {
	generation := request.Generation

	// The sweep, or a poll from another request, links back to the request that submitted the
	// generation. A missing or unreadable trace context extracts as the caller's own, so no link.
	var options []trace.SpanStartOption

	submitter := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(
		ctx, propagation.MapCarrier{"traceparent": lo.FromPtr(generation.Traceparent)},
	))
	if submitter.TraceID() != trace.SpanContextFromContext(ctx).TraceID() {
		options = append(options, trace.WithLinks(trace.Link{SpanContext: submitter}))
	}

	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck", options...)
	defer span.End()

	// A check outlives the call that triggered it: the provider may hold paid work whose id or answer
	// must be recorded. A start waits out the write of its call id.
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), lo.Ternary(request.Start != nil, startCheckTimeout, checkTimeout),
	)
	defer cancel()

	span.SetAttributes(attribute.String("generation.id", generation.ID.String()))

	var (
		checked *dao.Generation
		err     error
	)

	switch {
	case generation.SettledAt != nil:
		return generation, nil
	case generation.ProviderCallID != nil:
		checked, err = service.observe(ctx, generation)
	case generation.StartRequestedAt != nil && request.Start != nil:
		checked, err = service.start(ctx, generation, request.Start)
	case generation.StartRequestedAt != nil:
		checked, err = service.resolveStart(ctx, generation)
	case generation.CancelRequestedAt != nil:
		checked, err = service.settle(ctx, generation, &connectors.Call{State: connectors.CallCancelled})
	default:
		// An earlier release left the generation pending between two starts, its payload out of reach.
		// The caller resends its request to run it.
		checked, err = service.settleFailure(
			ctx, generation, failureStartFailed, dao.GenerationFailureNotStarted, "no start intent to send", nil,
		)
	}

	if errors.Is(err, dao.ErrGenerationChanged) {
		// Another check committed first. Its result is the answer.
		checked, err = service.daoGet.Exec(ctx, &dao.GenerationGetRequest{
			ID: generation.ID, OwnerID: generation.OwnerID,
		})
	}

	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return checked, nil
}

// start sends the provider call of a start intent the submit just recorded, and stops the call at
// once when its owner asked to cancel meanwhile.
func (service *GenerationCheck) start(
	ctx context.Context, intent *dao.Generation, start *GenerationCheckStart,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(start)")
	defer span.End()

	// A deployment that dropped the provider leaves the generation unable to run.
	connector, err := service.registry.Async(intent.Provider)
	if err != nil {
		settled, settleErr := service.settleFailure(
			ctx, intent, failureProviderUnavailable, dao.GenerationFailureNotStarted, err.Error(), nil,
		)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	// A process frozen since recording the intent may find it already settled as unknown by another
	// check. Sending it now would pay for a call nothing reads.
	if time.Since(start.IntentAt) >= startTimeout {
		settled, settleErr := service.settleFailure(
			ctx, intent, failureStartFailed, dao.GenerationFailureNotStarted, "start intent too old to send", nil,
		)
		if settleErr != nil {
			return nil, otel.ReportError(span, fmt.Errorf("settle stale start: %w", settleErr))
		}

		return settled, nil
	}

	// A streamed start waits for the provider to begin the call, which takes its queue time.
	startCtx, cancelStart := context.WithTimeout(ctx, lo.Ternary(intent.Streamed, streamStartTimeout, startTimeout))
	defer cancelStart()

	// A streamed start names its call before the provider begins it. Recorded then, the call stays
	// tracked by a process stopped during the rest of the hold.
	var (
		recorded  *dao.Generation
		recordErr error
	)

	record := func(call *connectors.Call) error {
		recorded, recordErr = service.recordCall(ctx, connector, intent, start.IntentAt, call.ID)

		return recordErr
	}

	call, err := connector.Start(startCtx, &connectors.AsyncStartRequest{
		Payload: start.Payload, Stream: intent.Streamed, Accepted: record,
	})

	switch {
	case recordErr != nil:
		return nil, otel.ReportError(span, fmt.Errorf("record provider call: %w", recordErr))
	case recorded == nil && err != nil:
		failed, failErr := service.failStart(ctx, intent, err)
		if failErr != nil {
			return nil, otel.ReportError(span, failErr)
		}

		return failed, nil
	case recorded == nil:
		err = record(call)
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("record provider call: %w", err))
		}
	}

	// A cancel requested while the start was in flight found no call to stop. Stopped now, the call
	// costs nothing more and frees its owner's slot, instead of running until the next check.
	if recorded.CancelRequestedAt == nil {
		return recorded, nil
	}

	stopped, err := service.observe(ctx, recorded)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return stopped, nil
}

// recordCall attaches an accepted call to its intent. The generation may have settled while the
// start was in flight, or the write may have failed without landing. A call the generation does not
// carry then belongs to nothing, so it is stopped.
//
// The write is patient. A migration holding a lock, or a lost connection, delays it, so it goes on
// with a growing pause until recordPatience after the intent. A check may settle the intent as
// unknown only once startOutcomeUnknownAfter has passed, which outlasts every write.
func (service *GenerationCheck) recordCall(
	ctx context.Context, connector connectors.Async, intent *dao.Generation, intentAt time.Time, callID string,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(recordCall)")
	defer span.End()

	write := func() (*dao.Generation, error) {
		recordCtx, cancel := context.WithTimeout(ctx, persistenceTimeout)
		defer cancel()

		return service.daoRecord.Exec(recordCtx, &dao.GenerationRecordProviderCallRequest{
			ID: intent.ID, ProviderCallID: callID,
		})
	}

	recorded, err := write()
	movedOn := errors.Is(err, dao.ErrGenerationChanged)

	// A write that failed without an answer may still commit: the database finishes a statement its
	// client gave up on. The next write waits behind it on the row, then lands itself if the first never
	// did. A write that finds the generation moved on ends the retries.
	deadline := intentAt.Add(recordPatience)
	pause := recordPause

	for err != nil && !errors.Is(err, dao.ErrGenerationChanged) && time.Now().Add(pause).Before(deadline) {
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return nil, otel.ReportError(span, errors.Join(err, ctx.Err()))
		}

		recorded, err = write()
		pause = min(pause+pause, recordPauseCeiling)
	}

	if err == nil {
		return recorded, nil
	}

	// Any write may have landed after all, and a later one then finds the generation moved on. When the
	// last write times out, the read may still miss it about to land. The call is then stopped, and the
	// generation fails once that write lands.
	if !movedOn {
		getCtx, cancel := context.WithTimeout(ctx, persistenceTimeout)
		current, getErr := service.daoGet.Exec(getCtx, &dao.GenerationGetRequest{ID: intent.ID, OwnerID: intent.OwnerID})

		cancel()

		switch {
		case getErr != nil:
			// A generation that cannot be read may carry the call, which is then left running.
			return nil, otel.ReportError(span, err)
		case lo.FromPtr(current.ProviderCallID) == callID:
			return current, nil
		}
	}

	service.stopOrphan(ctx, connector, intent, callID)

	return nil, otel.ReportError(span, err)
}

// failStart settles a start the provider never accepted, on its first outcome. Nothing sends it
// again: the caller reads the failure class and decides whether to resend its request.
func (service *GenerationCheck) failStart(
	ctx context.Context, intent *dao.Generation, cause error,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(failStart)")
	defer span.End()

	// A start never sent, or turned away before the provider accepted it, ran nothing.
	message, class := failureStartFailed, dao.GenerationFailureNotStarted

	var (
		rejection *connectors.RejectionError
		response  json.RawMessage
	)

	switch {
	case errors.As(cause, &rejection):
		message, class, response = failureRejected, dao.GenerationFailureRejected, rejection.Response
	case errors.Is(cause, connectors.ErrStartAmbiguous):
		message, class = failureOutcomeUnknown, dao.GenerationFailureOutcomeUnknown
	}

	settled, err := service.settleFailure(ctx, intent, message, class, cause.Error(), response)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// resolveStart settles a start intent that never recorded an id, once its start cannot still be in
// flight. The provider may have accepted the call, so nothing sends it again.
func (service *GenerationCheck) resolveStart(
	ctx context.Context, generation *dao.Generation,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(resolveStart)")
	defer span.End()

	// Both timestamps come from the database clock.
	if generation.ReadAt.Sub(*generation.StartRequestedAt) < startOutcomeUnknownAfter {
		return generation, nil
	}

	settled, err := service.settleFailure(
		ctx, generation, failureOutcomeUnknown, dao.GenerationFailureOutcomeUnknown,
		"start intent recorded without a provider call id", nil,
	)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// observe asks the provider once about a known operation, and settles it when terminal.
func (service *GenerationCheck) observe(ctx context.Context, generation *dao.Generation) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(observe)")
	defer span.End()

	// The call runs on, out of this service's reach: it may still be paid for.
	connector, err := service.registry.Async(generation.Provider)
	if err != nil {
		settled, settleErr := service.settleFailure(
			ctx, generation, failureProviderUnavailable, dao.GenerationFailureOutcomeUnknown, err.Error(), nil,
		)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	call, stopSince, err := service.ask(ctx, connector, generation)
	if err == nil {
		span.SetAttributes(attribute.String("provider.state", string(call.State)))
	}

	// A call this check cannot follow fails its generation: starting another call would pay twice.
	var (
		message, reason string
		class           dao.GenerationFailure
	)

	switch {
	case err == nil && call.State.Terminal():
		// Settled below.
	case errors.Is(err, connectors.ErrResponseTooLarge):
		message, class, reason = failureResponseTooLarge, dao.GenerationFailureOutcomeUnknown, err.Error()
	case err != nil && !errors.Is(err, connectors.ErrRetryable):
		// Unreadable, typically gone past the provider's retention window.
		message, class, reason = failureResultLost, dao.GenerationFailureProviderFailed, err.Error()
	case stopSince == nil || generation.ReadAt.Sub(*stopSince) < stopRetryWindow:
		// A transient failure, or a stop the provider has not applied yet: the next check asks again.
		return generation, nil
	case err != nil:
		// A stop still failing a while after it was first wanted is given up on. The call may still
		// run, so the generation keeps its slot a while.
		message, class, reason = failureNotStopped, dao.GenerationFailureOutcomeUnknown, err.Error()
	default:
		message, class = failureNotStopped, dao.GenerationFailureOutcomeUnknown
		reason = fmt.Sprintf("the stop left the call %s", call.State)
	}

	if message != "" {
		settled, settleErr := service.settleFailure(ctx, generation, message, class, reason, nil)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	// Another check stopped the call, maybe because its owner asked after this check read the
	// generation. The outcome is labeled from the cancel request as it stands now. Only that request
	// comes from the fresh read, so the settle stays fenced on the call this check observed.
	if call.State == connectors.CallCancelled && generation.CancelRequestedAt == nil && stopSince == nil {
		current, getErr := service.daoGet.Exec(ctx, &dao.GenerationGetRequest{ID: generation.ID, OwnerID: generation.OwnerID})
		if getErr != nil {
			return nil, otel.ReportError(span, fmt.Errorf("re-read generation: %w", getErr))
		}

		labeled := *generation
		labeled.CancelRequestedAt = current.CancelRequestedAt
		generation = &labeled
	}

	// A failed call settles on its first outcome, retryable or not: the caller decides whether to resend.
	recorded, err := service.settle(ctx, generation, call)

	// The database refuses the same answer on every check, such as usage that breaks a constraint. Once
	// the call is older than the run ceiling and the stop retry window together, the generation fails
	// without the answer. Any other failure, such as a lock wait or a lost connection, is retried by the
	// next check.
	if errors.Is(err, dao.ErrRefused) &&
		generation.ReadAt.Sub(*generation.StartRequestedAt) >= runCeiling+stopRetryWindow {
		recorded, err = service.settleFailure(
			ctx, generation, failureNotRecorded, dao.GenerationFailureProviderFailed, err.Error(), nil,
		)
	}

	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return recorded, nil
}

// ask reads the call or stops it. It also returns when the stop was first wanted, or nil when it only
// read.
//
// A call is stopped when its owner asks, and past the run ceiling whether they asked or not. A call
// the provider never finishes, or a key it no longer accepts, then cannot hold the owner's slot
// forever.
func (service *GenerationCheck) ask(
	ctx context.Context, connector connectors.Async, generation *dao.Generation,
) (*connectors.Call, *time.Time, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(ask)")
	defer span.End()

	// Every timestamp here comes from the database clock. A recorded call always has its start.
	start := *generation.StartRequestedAt
	since := generation.CancelRequestedAt
	age := generation.ReadAt.Sub(start)

	if ceiling := start.Add(runCeiling); age >= runCeiling && (since == nil || ceiling.Before(*since)) {
		since = &ceiling
	}

	if since == nil {
		call, err := connector.Get(ctx, *generation.ProviderCallID)
		if err != nil {
			return nil, nil, otel.ReportError(span, err)
		}

		// The provider can leave a streamed call queued for good when its creation stream was left
		// early. One still queued past its ceiling is stopped like an overdue call.
		if !generation.Streamed || call.State != connectors.CallQueued || age < streamQueuedCeiling {
			return call, nil, nil
		}

		since = new(start.Add(streamQueuedCeiling))
	}

	call, err := connector.Cancel(ctx, *generation.ProviderCallID)
	if err != nil {
		return nil, since, otel.ReportError(span, err)
	}

	return call, since, nil
}

// settle records a terminal provider state as the generation's outcome.
func (service *GenerationCheck) settle(
	ctx context.Context, generation *dao.Generation, call *connectors.Call,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(settle)")
	defer span.End()

	status, message := settleOutcomeOf(generation, call)

	span.SetAttributes(attribute.String("generation.status", string(status)))

	// The provider ran the call, so what failed it was the provider's: a failed call, or one stopped
	// past its ceiling. Only a failed call carries the provider's reason. One this service stopped is
	// explained by the outcome.
	var class *dao.GenerationFailure

	if status == dao.GenerationStatusFailed {
		class = new(dao.GenerationFailureProviderFailed)
		reason := lo.Ternary(call.State == connectors.CallFailed, call.Reason, *message)
		service.logger.Err(ctx, failureLog(generation, reason))
	}

	settled, err := service.commit(ctx, generation, status, message, class, call)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// settleFailure records a terminal failure in this service's words with its class, and the provider's
// answer when it gave one, then logs its cause for operators. It takes a deadline of its own, so a
// check that spent its budget on a slow start still records the failure.
func (service *GenerationCheck) settleFailure(
	ctx context.Context, generation *dao.Generation, message string, class dao.GenerationFailure, cause string,
	response json.RawMessage,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(settleFailure)")
	defer span.End()

	service.logger.Err(ctx, failureLog(generation, cause))

	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
	defer cancel()

	settled, err := service.commit(
		settleCtx, generation, dao.GenerationStatusFailed, &message, &class, &connectors.Call{Response: response},
	)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// commit settles the generation with the provider's answer and what the call consumed, together.
//
// One transaction, because a terminal transition without its usage is a billed call nothing counts.
// The provider call runs before and outside it, so no pooled connection stays pinned while the
// provider answers.
func (service *GenerationCheck) commit(
	ctx context.Context, generation *dao.Generation, status dao.GenerationStatus, message *string,
	class *dao.GenerationFailure, call *connectors.Call,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(commit)")
	defer span.End()

	var settled *dao.Generation

	err := service.transactor.WithinTx(ctx, func(ctx context.Context) error {
		var err error

		daoRequest := &dao.GenerationSettleRequest{
			ID:               generation.ID,
			StartRequestedAt: generation.StartRequestedAt,
			ProviderCallID:   generation.ProviderCallID,
			Status:           status,
			Error:            message,
			Failure:          class,
			Retention:        service.generations.Retention,
			OutcomeUnknown:   lo.FromPtr(class) == dao.GenerationFailureOutcomeUnknown,
		}

		settled, err = service.daoSettle.Exec(ctx, daoRequest)
		if err != nil {
			return fmt.Errorf("settle generation: %w", err)
		}

		err = service.recordAttempt(ctx, generation, call.Response)
		if err != nil {
			return err
		}

		return service.recordUsage(ctx, generation, call)
	})
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// recordUsage writes the attempt's Usage Record. A call the provider reports no usage for has
// nothing to count.
func (service *GenerationCheck) recordUsage(
	ctx context.Context, generation *dao.Generation, call *connectors.Call,
) error {
	if call.Usage == nil {
		return nil
	}

	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(recordUsage)")
	defer span.End()

	daoRequest := &dao.GenerationUsageInsertRequest{
		GenerationID:      generation.ID,
		OwnerID:           generation.OwnerID,
		Purpose:           generation.Purpose,
		Provider:          generation.Provider,
		Model:             call.Model,
		Class:             call.Class,
		Tier:              lo.EmptyableToPtr(call.Tier),
		InputTokens:       call.Usage.InputTokens,
		CachedInputTokens: call.Usage.CachedInputTokens,
		CacheWriteTokens:  call.Usage.CacheWriteTokens,
		OutputTokens:      call.Usage.OutputTokens,
		ReasoningTokens:   call.Usage.ReasoningTokens,
		// A provider that reports no start is timed from when this service sent the call.
		StartedAt:   lo.Ternary(call.StartedAt.IsZero(), lo.FromPtr(generation.StartRequestedAt), call.StartedAt),
		CompletedAt: call.CompletedAt,
	}

	_, err := service.daoUsage.Exec(ctx, daoRequest)

	// The stop of an orphaned call may have recorded this attempt's usage already, which is no failure.
	if err != nil && !errors.Is(err, dao.ErrGenerationUsageExists) {
		return otel.ReportError(span, fmt.Errorf("record usage: %w", err))
	}

	return nil
}

// recordAttempt keeps the provider's answer to the generation's call. A call that never produced one
// has nothing to keep.
func (service *GenerationCheck) recordAttempt(
	ctx context.Context, generation *dao.Generation, response json.RawMessage,
) error {
	if response == nil {
		return nil
	}

	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(recordAttempt)")
	defer span.End()

	daoRequest := &dao.GenerationAttemptInsertRequest{
		GenerationID: generation.ID,
		Response:     response,
	}

	_, err := service.daoAttempt.Exec(ctx, daoRequest)
	if err != nil {
		return otel.ReportError(span, fmt.Errorf("record attempt: %w", err))
	}

	return nil
}

// stopOrphan cancels an accepted call its generation does not carry, and records what it consumed.
// Best effort: a failure is logged, and the call runs to completion unread.
func (service *GenerationCheck) stopOrphan(
	ctx context.Context, connector connectors.Async, generation *dao.Generation, callID string,
) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(stopOrphan)")
	defer span.End()

	// The connector bounds its own cancel, so the write keeps a deadline of its own.
	call, err := connector.Cancel(ctx, callID)
	if err == nil {
		usageCtx, cancel := context.WithTimeout(ctx, persistenceTimeout)
		defer cancel()

		// The orphan ran on the provider's side: what it consumed still counts against the caps.
		err = service.recordUsage(usageCtx, generation, call)
	}

	if err != nil {
		service.logger.Err(ctx, fmt.Sprintf(
			"generation %s: stop orphaned provider call %s: %v", generation.ID, callID, err,
		))
	}
}

// failureLog is the server log line recording why a generation failed. The provider's own account of
// a failure stays in these logs.
func failureLog(generation *dao.Generation, reason string) string {
	return fmt.Sprintf("generation %s failed: %s", generation.ID, cmp.Or(reason, "provider returned no failure reason"))
}

// settleOutcomeOf maps a terminal provider state onto the status to land in, with the message a
// caller reads.
//
// A failed call is not a free one: its response carries what it consumed, which is why settle still
// records it.
func settleOutcomeOf(generation *dao.Generation, call *connectors.Call) (dao.GenerationStatus, *string) {
	switch call.State {
	case connectors.CallSucceeded:
		return dao.GenerationStatusSucceeded, nil
	case connectors.CallCancelled:
		// Only this service cancels its calls: one its owner never asked to stop ran past the ceiling.
		if generation.CancelRequestedAt == nil {
			return dao.GenerationStatusFailed, new(failureOverdue)
		}

		return dao.GenerationStatusCancelled, new(generationCancelledReason)
	default:
		// A failed call, or, unreachable since observe settles only a terminal state, a status the
		// provider added: failing keeps it from stranding the generation.
		return dao.GenerationStatusFailed, new(failureCallFailed)
	}
}

const (
	// startTimeout bounds a start call. Background mode answers once the provider queues the work,
	// usually within a second. A start cut after its request left is ambiguous and may run unread, so a
	// slow provider gets time to answer. A deploy that stops a start in flight loses it either way.
	startTimeout = 20 * time.Second
	// persistenceTimeout bounds a write that runs detached from a cancelled caller. The wait for a
	// pooled connection counts against it, which a burst of large submits stretches to seconds. It
	// outlasts the three seconds a migration waits for a lock too: the database finishes a statement
	// its client gave up on, but never commits such a transaction.
	persistenceTimeout = 4 * time.Second
	// streamStartTimeout bounds a streamed start, which waits for the provider to begin the call.
	streamStartTimeout = 30 * time.Second
	// streamQueuedCeiling is how long a streamed call may stay queued before it is stopped.
	streamQueuedCeiling = 5 * time.Minute
	// recordPatience is how long after its intent a start keeps writing its call id. A migration's
	// lock then costs the call latency instead of the call itself.
	recordPatience = 2 * time.Minute
	// recordPause is the first pause between two writes of a call id, doubled after each write up to
	// recordPauseCeiling.
	recordPause        = 250 * time.Millisecond
	recordPauseCeiling = 5 * time.Second
	// startOutcomeUnknownAfter is how old a start intent without an id must be before no start can
	// still be in flight for it. A start leaves within startTimeout of its intent, and its last write
	// begins before recordPatience and lasts persistenceTimeout, which stays under it.
	startOutcomeUnknownAfter = 3 * time.Minute
	// checkTimeout bounds a whole check, detached from its caller. A read takes up to the connector's
	// ten seconds, three times for a stuck streamed call whose stop finds it finished. The commit comes
	// on top.
	checkTimeout = time.Minute
	// startCheckTimeout bounds a check that sends a call: the writes of its call id until
	// recordPatience, then a check of the recorded call when its owner cancelled meanwhile.
	startCheckTimeout = recordPatience + checkTimeout
	// stopRetryWindow is how long a stop that fails, or that the provider has not applied, is asked
	// again from when it was first wanted, before the call is given up on.
	stopRetryWindow = 10 * time.Minute
	// runCeiling bounds how long a call may run once started. Long reasoning runs take tens of
	// minutes; past this, the call is stopped and the generation fails.
	runCeiling = 2 * time.Hour
)

// What a caller reads about a generation that did not succeed. The provider's own account of a
// failure is in the attempt's response, or in server logs when it sent none.
const (
	generationCancelledReason  = "generation cancelled"
	failureOutcomeUnknown      = "the provider may have accepted the call, but its outcome is unknown"
	failureResultLost          = "the provider no longer holds the result"
	failureStartFailed         = "the provider call could not be started"
	failureCallFailed          = "the provider failed the call"
	failureRejected            = "the provider rejected the request"
	failureProviderUnavailable = "no connector serves the generation's provider"
	failureOverdue             = "the provider did not finish the call in time, so it was stopped"
	failureNotStopped          = "the call could not be stopped, and the provider may still run it"
	failureResponseTooLarge    = "the provider's response is larger than the 24 MiB this service reads"
	failureNotRecorded         = "the provider's answer could not be recorded"
)
