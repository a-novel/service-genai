package core

import (
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

// GenerationCheckDaoBeginStart takes the next attempt.
type GenerationCheckDaoBeginStart interface {
	Exec(ctx context.Context, request *dao.GenerationBeginStartRequest) (*dao.Generation, error)
}

// GenerationCheckDaoReleaseStart gives back an attempt the provider never accepted.
type GenerationCheckDaoReleaseStart interface {
	Exec(ctx context.Context, request *dao.GenerationReleaseStartRequest) (*dao.Generation, error)
}

// GenerationCheckDaoRecordProviderCall attaches the accepted provider operation.
type GenerationCheckDaoRecordProviderCall interface {
	Exec(ctx context.Context, request *dao.GenerationRecordProviderCallRequest) (*dao.Generation, error)
}

// GenerationCheckDaoSettle records a terminal outcome.
type GenerationCheckDaoSettle interface {
	Exec(ctx context.Context, request *dao.GenerationSettleRequest) (*dao.Generation, error)
}

// GenerationCheckDaoRequeue returns a retryably failed attempt to the queue.
type GenerationCheckDaoRequeue interface {
	Exec(ctx context.Context, request *dao.GenerationRequeueRequest) (*dao.Generation, error)
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
}

// A GenerationCheck looks at one generation and applies whatever transition is due: start its
// provider call, or ask the provider once and record what changed.
//
// No process owns a generation. Every transition is a conditional write fenced by what the check
// read, so concurrent checks are safe and a check interrupted at any point is redone by the next.
type GenerationCheck struct {
	daoBeginStart   GenerationCheckDaoBeginStart
	daoReleaseStart GenerationCheckDaoReleaseStart
	daoRecord       GenerationCheckDaoRecordProviderCall
	daoSettle       GenerationCheckDaoSettle
	daoRequeue      GenerationCheckDaoRequeue
	daoAttempt      GenerationCheckDaoAttemptInsert
	daoUsage        GenerationCheckDaoUsageInsert
	daoGet          GenerationCheckDaoGet
	registry        connectors.Registry
	transactor      transaction.Transactor
	logger          GenerationCheckLogger
	generations     config.Generations
}

// NewGenerationCheck returns a new GenerationCheck service.
func NewGenerationCheck(
	daoBeginStart GenerationCheckDaoBeginStart,
	daoReleaseStart GenerationCheckDaoReleaseStart,
	daoRecord GenerationCheckDaoRecordProviderCall,
	daoSettle GenerationCheckDaoSettle,
	daoRequeue GenerationCheckDaoRequeue,
	daoAttempt GenerationCheckDaoAttemptInsert,
	daoUsage GenerationCheckDaoUsageInsert,
	daoGet GenerationCheckDaoGet,
	registry connectors.Registry,
	transactor transaction.Transactor,
	logger GenerationCheckLogger,
	generations config.Generations,
) *GenerationCheck {
	return &GenerationCheck{
		daoBeginStart:   daoBeginStart,
		daoReleaseStart: daoReleaseStart,
		daoRecord:       daoRecord,
		daoSettle:       daoSettle,
		daoRequeue:      daoRequeue,
		daoAttempt:      daoAttempt,
		daoUsage:        daoUsage,
		daoGet:          daoGet,
		registry:        registry,
		transactor:      transactor,
		logger:          logger,
		generations:     generations,
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

	// A check outlives the call that triggered it. A poll that elected it already stamped the
	// generation, so a check cut by the caller's deadline would leave it stale; and from a start's
	// intent on, the provider may hold paid work whose id must be recorded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
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
	case generation.StartRequestedAt != nil:
		checked, err = service.resolveStart(ctx, generation)
	case generation.CancelRequestedAt != nil:
		checked, err = service.settle(ctx, generation, &connectors.Call{State: connectors.CallCancelled})
	default:
		checked, err = service.start(ctx, generation)
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

// providerUnavailable settles a generation no connector runs any more, as a deployment that drops a
// provider leaves it: it cannot progress.
func (service *GenerationCheck) providerUnavailable(
	ctx context.Context, generation *dao.Generation, cause error,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(providerUnavailable)")
	defer span.End()

	service.logFailure(ctx, generation, cause.Error())

	settled, err := service.settleFailure(ctx, generation, failureProviderUnavailable, nil)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// start sends the provider call for the next attempt.
func (service *GenerationCheck) start(ctx context.Context, generation *dao.Generation) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(start)")
	defer span.End()

	connector, err := service.registry.Async(generation.Provider)
	if err != nil {
		settled, settleErr := service.providerUnavailable(ctx, generation, err)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	// Taken before the intent is recorded, so it can only overstate the intent's age.
	began := time.Now()

	intent, err := service.daoBeginStart.Exec(ctx, &dao.GenerationBeginStartRequest{ID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("begin start: %w", err))
	}

	span.SetAttributes(attribute.Int("generation.attempt", int(intent.Attempt)))

	// A streamed start waits for the provider to begin the call, which takes its queue time.
	startCtx, cancelStart := context.WithTimeout(ctx, lo.Ternary(intent.Streamed, streamStartTimeout, startTimeout))
	defer cancelStart()

	// A process frozen since recording intent may find the attempt already settled as unknown by
	// another check. Sending it now would pay for a call nothing reads.
	if time.Since(began) >= startTimeout {
		released, releaseErr := service.releaseStart(ctx, intent, 0)
		if releaseErr != nil {
			return nil, otel.ReportError(span, fmt.Errorf("release stale start: %w", releaseErr))
		}

		return released, nil
	}

	call, err := connector.Start(startCtx, &connectors.AsyncStartRequest{Payload: intent.Payload, Stream: intent.Streamed})
	if err != nil {
		failed, failErr := service.failStart(ctx, intent, err)
		if failErr != nil {
			return nil, otel.ReportError(span, failErr)
		}

		return failed, nil
	}

	recordCtx, cancelRecord := context.WithTimeout(ctx, persistenceTimeout)
	defer cancelRecord()

	recorded, err := service.daoRecord.Exec(recordCtx, &dao.GenerationRecordProviderCallRequest{
		ID: intent.ID, Attempt: intent.Attempt, ProviderCallID: call.ID,
	})
	if err != nil {
		// The generation settled while the start was in flight, or the write failed and may not have
		// landed. An accepted call the generation does not carry belongs to nothing.
		if errors.Is(err, dao.ErrGenerationChanged) || !service.carriesCall(ctx, intent, call.ID) {
			service.stopOrphan(ctx, connector, intent, call.ID)
		}

		return nil, otel.ReportError(span, fmt.Errorf("record provider call: %w", err))
	}

	return recorded, nil
}

// releaseStart gives back an attempt whose call was never sent. It runs detached, so it carries its
// own deadline: a stalled database must not hold the process past shutdown.
func (service *GenerationCheck) releaseStart(
	ctx context.Context, intent *dao.Generation, delay time.Duration,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(releaseStart)")
	defer span.End()

	releaseCtx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()

	released, err := service.daoReleaseStart.Exec(releaseCtx, &dao.GenerationReleaseStartRequest{
		ID: intent.ID, Attempt: intent.Attempt, Delay: delay,
	})
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return released, nil
}

// carriesCall re-reads a generation whose call id write failed: the write may have landed anyway.
// A generation that cannot be read counts as carrying the call, so a call it may track is never
// stopped.
func (service *GenerationCheck) carriesCall(ctx context.Context, intent *dao.Generation, callID string) bool {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(carriesCall)")
	defer span.End()

	getCtx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()

	// The read's own span carries its failure; here it is a handled outcome.
	current, err := service.daoGet.Exec(getCtx, &dao.GenerationGetRequest{ID: intent.ID, OwnerID: intent.OwnerID})
	if err != nil {
		return true
	}

	return lo.FromPtr(current.ProviderCallID) == callID
}

// failStart decides what a rejected start costs. Only a definitive refusal gives the attempt back.
func (service *GenerationCheck) failStart(
	ctx context.Context, intent *dao.Generation, cause error,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(failStart)")
	defer span.End()

	if errors.Is(cause, connectors.ErrRetryable) && intent.StartRequestedAt.Sub(intent.CreatedAt) < startRetryCeiling {
		released, err := service.releaseStart(ctx, intent, retryDelay)
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("release start: %w", err))
		}

		return released, nil
	}

	failure := failureStartFailed

	var (
		rejection *connectors.RejectionError
		response  json.RawMessage
	)

	switch {
	case errors.As(cause, &rejection):
		failure, response = failureRejected, rejection.Response
	case errors.Is(cause, connectors.ErrStartAmbiguous):
		failure = failureOutcomeUnknown
	}

	service.logFailure(ctx, intent, cause.Error())

	settled, err := service.settleFailure(ctx, intent, failure, response)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// resolveStart settles an attempt whose start never recorded an id, once that start cannot still
// be in flight. The provider may have accepted it, so nothing starts the attempt again.
func (service *GenerationCheck) resolveStart(
	ctx context.Context, generation *dao.Generation,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(resolveStart)")
	defer span.End()

	// Both timestamps come from the database clock.
	if generation.CheckedAt.Sub(*generation.StartRequestedAt) < startOutcomeUnknownAfter {
		return generation, nil
	}

	service.logFailure(ctx, generation, "start intent recorded without a provider call id")

	settled, err := service.settleFailure(ctx, generation, failureOutcomeUnknown, nil)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// observe asks the provider once about a known operation, and settles it when terminal.
func (service *GenerationCheck) observe(ctx context.Context, generation *dao.Generation) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(observe)")
	defer span.End()

	connector, err := service.registry.Async(generation.Provider)
	if err != nil {
		settled, settleErr := service.providerUnavailable(ctx, generation, err)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	// How long the call has run, by the database clock. A recorded call always has its start.
	age := generation.CheckedAt.Sub(*generation.StartRequestedAt)

	// Past the run ceiling the call is stopped, whether its owner asked or not: one the provider never
	// finishes, or a key it no longer accepts, must not hold the owner's slot forever.
	overdue := age >= runCeiling

	var call *connectors.Call

	if generation.CancelRequestedAt != nil || overdue {
		call, err = connector.Cancel(ctx, *generation.ProviderCallID)
	} else {
		call, err = connector.Get(ctx, *generation.ProviderCallID)
	}

	// The provider can leave a streamed call queued for good when its creation stream was left early.
	// One still queued past its ceiling is stopped like an overdue call.
	if err == nil && generation.Streamed && !overdue && generation.CancelRequestedAt == nil &&
		call.State == connectors.CallQueued && age >= streamQueuedCeiling {
		overdue = true
		call, err = connector.Cancel(ctx, *generation.ProviderCallID)
	}

	if errors.Is(err, connectors.ErrRetryable) && !overdue {
		// A transient failure is retried by the next check; the provider span holds the cause.
		return generation, nil
	}

	if err != nil {
		// The operation is unreadable, typically gone past the provider's retention window, or a call
		// past its ceiling could not be stopped and may still run. Starting another would pay twice.
		service.logFailure(ctx, generation, err.Error())

		failure := lo.Ternary(errors.Is(err, connectors.ErrRetryable), failureNotStopped, failureResultLost)

		settled, settleErr := service.settleFailure(ctx, generation, failure, nil)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	span.SetAttributes(attribute.String("provider.state", string(call.State)))

	if !call.State.Terminal() {
		return generation, nil
	}

	if call.State == connectors.CallFailed && call.Retryable && generation.Attempt < generation.MaxAttempts &&
		generation.CancelRequestedAt == nil {
		requeued, requeueErr := service.requeue(ctx, generation, call)
		if requeueErr != nil {
			return nil, otel.ReportError(span, requeueErr)
		}

		return requeued, nil
	}

	settled, err := service.settle(ctx, generation, call)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// requeue records the failed attempt's answer and usage, and authorizes a fresh call, together.
func (service *GenerationCheck) requeue(
	ctx context.Context, generation *dao.Generation, call *connectors.Call,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(requeue)")
	defer span.End()

	service.logFailure(ctx, generation, call.Reason)

	var requeued *dao.Generation

	err := service.transactor.WithinTx(ctx, func(ctx context.Context) error {
		var err error

		requeued, err = service.daoRequeue.Exec(ctx, &dao.GenerationRequeueRequest{
			ID:             generation.ID,
			Attempt:        generation.Attempt,
			ProviderCallID: *generation.ProviderCallID,
			Delay:          retryDelay,
		})
		if err != nil {
			return fmt.Errorf("requeue generation: %w", err)
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

	return requeued, nil
}

// settle records a terminal provider state as the generation's outcome.
func (service *GenerationCheck) settle(
	ctx context.Context, generation *dao.Generation, call *connectors.Call,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(settle)")
	defer span.End()

	status, message := settleOutcomeOf(generation, call)

	span.SetAttributes(attribute.String("generation.status", string(status)))

	if status == dao.GenerationStatusFailed {
		service.logFailure(ctx, generation, call.Reason)
	}

	settled, err := service.commit(ctx, generation, status, message, call)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// settleFailure records a terminal failure in this service's words, with the provider's answer when
// it gave one. It takes a deadline of its own, so a check that spent its budget on a slow start still
// records the failure.
func (service *GenerationCheck) settleFailure(
	ctx context.Context, generation *dao.Generation, message string, response json.RawMessage,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(settleFailure)")
	defer span.End()

	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
	defer cancel()

	settled, err := service.commit(
		settleCtx, generation, dao.GenerationStatusFailed, &message, &connectors.Call{Response: response},
	)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// commit settles the generation with the provider's answer and what the call consumed, together.
//
// One transaction, because a terminal transition without its usage is a billed call nothing counts.
// The provider call happened before and outside it: a transaction held open across a call would pin
// a pooled connection for its whole duration.
func (service *GenerationCheck) commit(
	ctx context.Context, generation *dao.Generation, status dao.GenerationStatus, message *string,
	call *connectors.Call,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(commit)")
	defer span.End()

	var settled *dao.Generation

	err := service.transactor.WithinTx(ctx, func(ctx context.Context) error {
		var err error

		settled, err = service.daoSettle.Exec(ctx, &dao.GenerationSettleRequest{
			ID:               generation.ID,
			Attempt:          generation.Attempt,
			StartRequestedAt: generation.StartRequestedAt,
			ProviderCallID:   generation.ProviderCallID,
			Status:           status,
			Error:            message,
			Retention:        service.generations.Retention,
			OutcomeUnknown:   outcomeUnknown[lo.FromPtr(message)],
		})
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
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(recordUsage)")
	defer span.End()

	if call.Usage == nil {
		return nil
	}

	_, err := service.daoUsage.Exec(ctx, &dao.GenerationUsageInsertRequest{
		GenerationID:      generation.ID,
		Attempt:           generation.Attempt,
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
	})

	// A replay of the same provider result finds the row already there, which is no failure.
	if err != nil && !errors.Is(err, dao.ErrGenerationUsageExists) {
		return otel.ReportError(span, fmt.Errorf("record usage: %w", err))
	}

	return nil
}

// recordAttempt keeps the provider's answer to the generation's current attempt. A call that never
// produced one has nothing to keep.
func (service *GenerationCheck) recordAttempt(
	ctx context.Context, generation *dao.Generation, response json.RawMessage,
) error {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(recordAttempt)")
	defer span.End()

	if response == nil {
		return nil
	}

	_, err := service.daoAttempt.Exec(ctx, &dao.GenerationAttemptInsertRequest{
		GenerationID: generation.ID,
		Attempt:      generation.Attempt,
		Response:     response,
	})

	if err != nil && !errors.Is(err, dao.ErrGenerationAttemptExists) {
		return otel.ReportError(span, fmt.Errorf("record attempt: %w", err))
	}

	return nil
}

// stopOrphan cancels an accepted call whose generation settled first, and records what it consumed.
// Best effort: a failure is logged, and the call runs to completion unread.
func (service *GenerationCheck) stopOrphan(
	ctx context.Context, connector connectors.Async, generation *dao.Generation, callID string,
) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(stopOrphan)")
	defer span.End()

	cancelCtx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()

	call, err := connector.Cancel(cancelCtx, callID)
	if err == nil {
		// The orphan ran on the provider's side: what it consumed still counts against the caps.
		err = service.recordUsage(cancelCtx, generation, call)
	}

	if err != nil {
		service.logger.Err(ctx, fmt.Sprintf(
			"generation %s attempt %d: stop orphaned provider call %s: %v",
			generation.ID, generation.Attempt, callID, err,
		))
	}
}

func (service *GenerationCheck) logFailure(ctx context.Context, generation *dao.Generation, reason string) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(logFailure)")
	defer span.End()

	if reason == "" {
		reason = "provider returned no failure reason"
	}

	service.logger.Err(ctx, fmt.Sprintf("generation %s failed: %s", generation.ID, reason))
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
	case connectors.CallFailed:
		return dao.GenerationStatusFailed, new(failureCallFailed)
	case connectors.CallRunning:
		fallthrough
	default:
		// Unreachable: observe only settles a terminal state. Failing keeps a provider that grows a
		// new status from stranding the generation.
		return dao.GenerationStatusFailed, new(failureCallFailed)
	}
}

const (
	// startTimeout bounds a start call. Background mode answers once the provider queues the work,
	// and the start plus its id write must finish inside Cloud Run's ten-second shutdown window.
	startTimeout = 5 * time.Second
	// persistenceTimeout bounds a write that runs detached from a cancelled caller.
	persistenceTimeout = 2 * time.Second
	// streamStartTimeout bounds a streamed start, which waits for the provider to begin the call.
	streamStartTimeout = 30 * time.Second
	// streamQueuedCeiling is how long a streamed call may stay queued before it is stopped.
	streamQueuedCeiling = 5 * time.Minute
	// startOutcomeUnknownAfter is how old a start intent without an id must be before no start can
	// still be in flight for it. Well above streamStartTimeout plus persistenceTimeout.
	startOutcomeUnknownAfter = time.Minute
	// checkTimeout bounds a whole check, detached from its caller. A start that proceeds takes under
	// startTimeout to begin, then at most streamStartTimeout plus persistenceTimeout; a read takes
	// the connector's ten seconds plus its commit.
	checkTimeout = 45 * time.Second
	// retryDelay postpones a fresh call after a rate limit or a retryable provider failure.
	retryDelay = 15 * time.Second
	// startRetryCeiling bounds how long a generation keeps retrying a start the provider turns away,
	// measured from its submission. Past it the generation fails, so a request the provider never
	// accepts cannot hold a concurrency slot forever.
	startRetryCeiling = 10 * time.Minute
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
	failureNotStopped          = "the provider did not finish the call in time, and it could not be stopped"
)

// outcomeUnknown holds the failures after which the provider may still run a call nothing tracks or
// meters, so admission keeps counting the generation for a while.
var outcomeUnknown = map[string]bool{failureOutcomeUnknown: true, failureNotStopped: true}
