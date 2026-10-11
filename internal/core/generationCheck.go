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

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// Dependencies of [GenerationCheck].
type (
	// GenerationCheckLogger records failures whose provider details must remain server-side.
	GenerationCheckLogger interface {
		Err(ctx context.Context, msg string, fields ...any)
	}
	// GenerationCheckBeginStartDao takes the next attempt.
	GenerationCheckBeginStartDao interface {
		Exec(ctx context.Context, request *dao.GenerationBeginStartRequest) (*dao.Generation, error)
	}
	// GenerationCheckReleaseStartDao gives back an attempt the provider never accepted.
	GenerationCheckReleaseStartDao interface {
		Exec(ctx context.Context, request *dao.GenerationReleaseStartRequest) (*dao.Generation, error)
	}
	// GenerationCheckRecordProviderCallDao attaches the accepted provider operation.
	GenerationCheckRecordProviderCallDao interface {
		Exec(ctx context.Context, request *dao.GenerationRecordProviderCallRequest) (*dao.Generation, error)
	}
	// GenerationCheckSettleDao records a terminal outcome.
	GenerationCheckSettleDao interface {
		Exec(ctx context.Context, request *dao.GenerationSettleRequest) (*dao.Generation, error)
	}
	// GenerationCheckRequeueDao returns a retryably failed attempt to the queue.
	GenerationCheckRequeueDao interface {
		Exec(ctx context.Context, request *dao.GenerationRequeueRequest) (*dao.Generation, error)
	}
	// GenerationCheckAttemptInsertDao records the provider's answer to one attempt.
	GenerationCheckAttemptInsertDao interface {
		Exec(ctx context.Context, request *dao.GenerationAttemptInsertRequest) (*dao.GenerationAttempt, error)
	}
	// GenerationCheckUsageInsertDao records what one attempt consumed.
	GenerationCheckUsageInsertDao interface {
		Exec(ctx context.Context, request *dao.GenerationUsageInsertRequest) (*dao.GenerationUsage, error)
	}
	// GenerationCheckGetDao re-reads a generation another check moved on.
	GenerationCheckGetDao interface {
		Exec(ctx context.Context, request *dao.GenerationGetRequest) (*dao.Generation, error)
	}
)

// GenerationCheckDaos are the data-access dependencies of a [GenerationCheck]. Grouping them
// prevents positional swaps between interfaces that all take a request and return a generation.
type GenerationCheckDaos struct {
	BeginStart   GenerationCheckBeginStartDao
	ReleaseStart GenerationCheckReleaseStartDao
	Record       GenerationCheckRecordProviderCallDao
	Settle       GenerationCheckSettleDao
	Requeue      GenerationCheckRequeueDao
	Attempt      GenerationCheckAttemptInsertDao
	Usage        GenerationCheckUsageInsertDao
	Get          GenerationCheckGetDao
}

// GenerationCheckConfig is what a [GenerationCheck] needs to run.
type GenerationCheckConfig struct {
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration `validate:"required"`
}

// GenerationCheckRequest carries the generation to check, as last read.
type GenerationCheckRequest struct {
	Generation *dao.Generation
}

// A GenerationCheck looks at one generation and applies whatever transition is due: start its
// provider call, or ask the provider once and record what changed.
//
// No process owns a generation. Every transition is a conditional write fenced by what the check
// read, so concurrent checks are safe and a check interrupted at any point is redone by the next.
type GenerationCheck struct {
	config     GenerationCheckConfig
	logger     GenerationCheckLogger
	registry   connectors.Registry
	transactor transaction.Transactor
	daos       GenerationCheckDaos
}

func NewGenerationCheck(
	config GenerationCheckConfig,
	logger GenerationCheckLogger,
	registry connectors.Registry,
	transactor transaction.Transactor,
	daos GenerationCheckDaos,
) (*GenerationCheck, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	if logger == nil {
		return nil, fmt.Errorf("%w: logger is required", ErrInvalidRequest)
	}

	return &GenerationCheck{
		config: config, logger: logger, registry: registry, transactor: transactor, daos: daos,
	}, nil
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
		checked, err = service.daos.Get.Exec(ctx, &dao.GenerationGetRequest{
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

	// From the intent on, the provider may hold paid work. Shutdown or a departing caller must not
	// cut the start short, or the call could be accepted with its id never recorded.
	ctx = context.WithoutCancel(ctx)

	// Taken before the intent is recorded, so it can only overstate the intent's age.
	began := time.Now()

	intent, err := service.daos.BeginStart.Exec(ctx, &dao.GenerationBeginStartRequest{ID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("begin start: %w", err))
	}

	span.SetAttributes(attribute.Int("generation.attempt", int(intent.Attempt)))

	startCtx, cancelStart := context.WithTimeout(ctx, startTimeout)
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

	call, err := connector.Start(startCtx, &connectors.AsyncStartRequest{Payload: intent.Payload})
	if err != nil {
		failed, failErr := service.failStart(ctx, intent, err)
		if failErr != nil {
			return nil, otel.ReportError(span, failErr)
		}

		return failed, nil
	}

	recordCtx, cancelRecord := context.WithTimeout(ctx, persistenceTimeout)
	defer cancelRecord()

	recorded, err := service.daos.Record.Exec(recordCtx, &dao.GenerationRecordProviderCallRequest{
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

	released, err := service.daos.ReleaseStart.Exec(releaseCtx, &dao.GenerationReleaseStartRequest{
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

	current, err := service.daos.Get.Exec(getCtx, &dao.GenerationGetRequest{ID: intent.ID, OwnerID: intent.OwnerID})
	if err != nil {
		_ = otel.ReportError(span, err)

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

	if errors.Is(cause, connectors.ErrRetryable) {
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

	var call *connectors.Call

	if generation.CancelRequestedAt != nil {
		call, err = connector.Cancel(ctx, *generation.ProviderCallID)
	} else {
		call, err = connector.Get(ctx, *generation.ProviderCallID)
	}

	if errors.Is(err, connectors.ErrRetryable) {
		// A transient failure is retried by the next check; the provider span holds the cause.
		return generation, nil
	}

	if err != nil {
		// The operation is unreadable, typically gone past the provider's retention window. Starting
		// another call would pay twice, so the generation fails.
		service.logFailure(ctx, generation, err.Error())

		settled, settleErr := service.settleFailure(ctx, generation, failureResultLost, nil)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	span.SetAttributes(attribute.String("provider.state", string(call.State)))

	if !call.State.Terminal() {
		return generation, nil
	}

	if call.State == connectors.CallFailed && call.Retryable && generation.Attempt < generation.MaxAttempts {
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

		requeued, err = service.daos.Requeue.Exec(ctx, &dao.GenerationRequeueRequest{
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

// settle records the outcome, the provider's answer and what the call consumed, together.
//
// One transaction, because a terminal transition without its usage is a billed call nothing counts.
// The provider call happened before and outside it: a transaction held open across a call would pin
// a pooled connection for its whole duration.
func (service *GenerationCheck) settle(
	ctx context.Context, generation *dao.Generation, call *connectors.Call,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(settle)")
	defer span.End()

	status, message := settleOutcomeOf(call)

	span.SetAttributes(attribute.String("generation.status", string(status)))

	if status == dao.GenerationStatusFailed {
		service.logFailure(ctx, generation, call.Reason)
	}

	var settled *dao.Generation

	err := service.transactor.WithinTx(ctx, func(ctx context.Context) error {
		var err error

		settled, err = service.daos.Settle.Exec(ctx, &dao.GenerationSettleRequest{
			ID:               generation.ID,
			Attempt:          generation.Attempt,
			StartRequestedAt: generation.StartRequestedAt,
			ProviderCallID:   generation.ProviderCallID,
			Status:           status,
			Error:            message,
			Retention:        service.config.Retention,
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

// settleFailure records a terminal failure in this service's words, with the provider's answer when
// it gave one. It runs detached, with its own deadline, so a departing caller cannot leave the
// generation half-settled.
func (service *GenerationCheck) settleFailure(
	ctx context.Context, generation *dao.Generation, message string, response json.RawMessage,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(settleFailure)")
	defer span.End()

	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
	defer cancel()

	var settled *dao.Generation

	err := service.transactor.WithinTx(settleCtx, func(ctx context.Context) error {
		var err error

		settled, err = service.daos.Settle.Exec(ctx, &dao.GenerationSettleRequest{
			ID:               generation.ID,
			Attempt:          generation.Attempt,
			StartRequestedAt: generation.StartRequestedAt,
			ProviderCallID:   generation.ProviderCallID,
			Status:           dao.GenerationStatusFailed,
			Error:            &message,
			Retention:        service.config.Retention,
		})
		if err != nil {
			return fmt.Errorf("settle failed generation: %w", err)
		}

		return service.recordAttempt(ctx, generation, response)
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

	_, err := service.daos.Usage.Exec(ctx, &dao.GenerationUsageInsertRequest{
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

	// A replay of the same provider result finds the row already there. That is the idempotent
	// outcome, not a failure.
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

	_, err := service.daos.Attempt.Exec(ctx, &dao.GenerationAttemptInsertRequest{
		GenerationID: generation.ID,
		Attempt:      generation.Attempt,
		Response:     response,
	})

	// A replay of the same provider result finds the row already there. That is the idempotent
	// outcome, not a failure.
	if err != nil && !errors.Is(err, dao.ErrGenerationAttemptExists) {
		return otel.ReportError(span, fmt.Errorf("record attempt: %w", err))
	}

	return nil
}

// stopOrphan cancels an accepted call whose generation settled first. Best effort: a failure is
// logged, and the call runs to completion unread.
func (service *GenerationCheck) stopOrphan(
	ctx context.Context, connector connectors.Async, generation *dao.Generation, callID string,
) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(stopOrphan)")
	defer span.End()

	cancelCtx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()

	_, err := connector.Cancel(cancelCtx, callID)
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
func settleOutcomeOf(call *connectors.Call) (dao.GenerationStatus, *string) {
	switch call.State {
	case connectors.CallSucceeded:
		return dao.GenerationStatusSucceeded, nil
	case connectors.CallCancelled:
		return dao.GenerationStatusCancelled, new(generationCancelledReason)
	case connectors.CallFailed:
		return dao.GenerationStatusFailed, new(failureCallFailed)
	case connectors.CallRunning:
		fallthrough
	default:
		// Unreachable: observe only settles a terminal state. Failing rather than panicking keeps a
		// provider that grows a new status from stranding the generation.
		return dao.GenerationStatusFailed, new(failureCallFailed)
	}
}

const (
	// startTimeout bounds a start call. Background mode answers once the provider queues the work,
	// and the start plus its id write must finish inside Cloud Run's ten-second shutdown window.
	startTimeout = 5 * time.Second
	// persistenceTimeout bounds a write that runs detached from a cancelled caller.
	persistenceTimeout = 2 * time.Second
	// startOutcomeUnknownAfter is how old a start intent without an id must be before no start can
	// still be in flight for it. Well above startTimeout plus persistenceTimeout.
	startOutcomeUnknownAfter = 30 * time.Second
	// retryDelay postpones a fresh call after a rate limit or a retryable provider failure.
	retryDelay = 15 * time.Second
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
)
