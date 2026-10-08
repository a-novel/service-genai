package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/transaction"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
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
	// GenerationCheckRestartDao discards an attempt started on an older provider configuration.
	GenerationCheckRestartDao interface {
		Exec(ctx context.Context, request *dao.GenerationRestartRequest) (*dao.Generation, error)
	}
	// GenerationCheckRequeueDao returns a retryably failed attempt to the queue.
	GenerationCheckRequeueDao interface {
		Exec(ctx context.Context, request *dao.GenerationRequeueRequest) (*dao.Generation, error)
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
	Restart      GenerationCheckRestartDao
	Usage        GenerationCheckUsageInsertDao
	Get          GenerationCheckGetDao
}

// GenerationCheckConfig is what a [GenerationCheck] needs to run.
type GenerationCheckConfig struct {
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration `validate:"required"`
	// ProviderName identifies the configured provider on usage records.
	ProviderName string `validate:"required"`
	// ProviderEpoch orders provider configurations. Raised on every provider switch.
	ProviderEpoch int32 `validate:"required,min=1"`
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
//
// A generation started on an older provider configuration is restarted on this one. One a newer
// configuration took over is left alone: during a rollout, replicas of both run side by side.
type GenerationCheck struct {
	config     GenerationCheckConfig
	logger     GenerationCheckLogger
	provider   lib.Provider
	transactor transaction.Transactor
	daos       GenerationCheckDaos
}

func NewGenerationCheck(
	config GenerationCheckConfig,
	logger GenerationCheckLogger,
	provider lib.Provider,
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
		config: config, logger: logger, provider: provider, transactor: transactor, daos: daos,
	}, nil
}

// Exec returns the generation as the check left it.
func (service *GenerationCheck) Exec(
	ctx context.Context, request *GenerationCheckRequest,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck")
	defer span.End()

	generation := request.Generation

	span.SetAttributes(attribute.String("generation.id", generation.ID.String()))

	var (
		checked *dao.Generation
		err     error
	)

	epoch := lo.FromPtr(generation.ProviderEpoch)

	switch {
	case generation.SettledAt != nil, epoch > service.config.ProviderEpoch:
		return generation, nil
	case epoch < service.config.ProviderEpoch && generation.StartRequestedAt != nil:
		checked, err = service.restart(ctx, generation)
	case generation.ProviderCallID != nil:
		checked, err = service.observe(ctx, generation)
	case generation.StartRequestedAt != nil:
		checked, err = service.resolveStart(ctx, generation)
	case generation.CancelRequestedAt != nil:
		checked, err = service.settle(ctx, generation, &lib.ProviderCall{State: lib.ProviderCallCancelled})
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

// start sends the provider call for the next attempt.
func (service *GenerationCheck) start(ctx context.Context, generation *dao.Generation) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(start)")
	defer span.End()

	var request generationRequest

	err := json.Unmarshal(generation.Request, &request)
	if err != nil {
		settled, settleErr := service.settleFailure(ctx, generation, failureUnreadableRequest)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	intent, err := service.daos.BeginStart.Exec(ctx, &dao.GenerationBeginStartRequest{
		ID: generation.ID, ProviderEpoch: service.config.ProviderEpoch,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("begin start: %w", err))
	}

	began := time.Now()

	span.SetAttributes(attribute.Int("generation.attempt", int(intent.Attempt)))

	// From here the provider may hold paid work. Shutdown must not cut the start short, or the call
	// could be accepted with its id never recorded.
	ctx = context.WithoutCancel(ctx)

	startCtx, cancelStart := context.WithTimeout(ctx, startTimeout)
	defer cancelStart()

	// A process frozen since recording intent may find the attempt already settled as unknown by
	// another check. Sending it now would pay for a call nothing reads.
	if time.Since(began) >= startTimeout {
		released, releaseErr := service.daos.ReleaseStart.Exec(ctx, &dao.GenerationReleaseStartRequest{
			ID: intent.ID, Attempt: intent.Attempt,
		})
		if releaseErr != nil {
			return nil, otel.ReportError(span, fmt.Errorf("release stale start: %w", releaseErr))
		}

		return released, nil
	}

	call, err := service.provider.Start(startCtx, &lib.ProviderStartRequest{
		Tier:         request.Tier,
		Instructions: request.Instructions,
		Input:        request.Input,
		OutputSchema: request.OutputSchema,
		EndUserID:    intent.OwnerID.String(),
		GenerationID: intent.ID.String(),
		Attempt:      intent.Attempt,
	})
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
	if errors.Is(err, dao.ErrGenerationChanged) {
		// The generation settled while the start was in flight. The accepted call belongs to nothing.
		service.stopOrphan(ctx, intent, call.ID)
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("record provider call: %w", err))
	}

	return recorded, nil
}

// failStart decides what a rejected start costs. Only a definitive refusal gives the attempt back.
func (service *GenerationCheck) failStart(
	ctx context.Context, intent *dao.Generation, cause error,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(failStart)")
	defer span.End()

	if errors.Is(cause, lib.ErrProviderRetryable) {
		released, err := service.daos.ReleaseStart.Exec(ctx, &dao.GenerationReleaseStartRequest{
			ID: intent.ID, Attempt: intent.Attempt, Delay: retryDelay,
		})
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("release start: %w", err))
		}

		return released, nil
	}

	failure := failureStartFailed

	var rejection *lib.ProviderRejectionError

	switch {
	case errors.As(cause, &rejection):
		failure = rejection.Failure
	case errors.Is(cause, lib.ErrProviderStartAmbiguous):
		failure = failureOutcomeUnknown
	}

	service.logFailure(ctx, intent, cause.Error())

	settled, err := service.settleFailure(ctx, intent, failure)
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

	settled, err := service.settleFailure(ctx, generation, failureOutcomeUnknown)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// observe asks the provider once about a known operation, and settles it when terminal.
func (service *GenerationCheck) observe(ctx context.Context, generation *dao.Generation) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(observe)")
	defer span.End()

	var (
		call *lib.ProviderCall
		err  error
	)

	if generation.CancelRequestedAt != nil {
		call, err = service.provider.Cancel(ctx, *generation.ProviderCallID)
	} else {
		call, err = service.provider.Get(ctx, *generation.ProviderCallID)
	}

	if errors.Is(err, lib.ErrProviderRetryable) {
		// A transient failure is retried by the next check; the provider span holds the cause.
		return generation, nil
	}

	if err != nil {
		// The operation is unreadable, typically gone past the provider's retention window. Starting
		// another call would pay twice, so the generation fails.
		service.logFailure(ctx, generation, err.Error())

		settled, settleErr := service.settleFailure(ctx, generation, failureResultLost)
		if settleErr != nil {
			return nil, otel.ReportError(span, settleErr)
		}

		return settled, nil
	}

	span.SetAttributes(attribute.String("provider.state", string(call.State)))

	if !call.State.Terminal() {
		return generation, nil
	}

	if call.State == lib.ProviderCallFailed && call.Retryable && generation.Attempt < generation.MaxAttempts {
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

// restart discards an attempt that runs on an older provider configuration, so the next check starts
// it again on this one. The older operation cannot be read or cancelled without its credentials: it
// runs on unobserved, and its spend goes unrecorded.
func (service *GenerationCheck) restart(ctx context.Context, generation *dao.Generation) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(restart)")
	defer span.End()

	service.logger.Err(ctx, fmt.Sprintf(
		"generation %s attempt %d: provider epoch %d superseded by %d, abandoning provider call %s",
		generation.ID, generation.Attempt, lo.FromPtr(generation.ProviderEpoch), service.config.ProviderEpoch,
		lo.FromPtrOr(generation.ProviderCallID, "(none recorded)"),
	))

	restarted, err := service.daos.Restart.Exec(ctx, &dao.GenerationRestartRequest{
		ID:             generation.ID,
		Attempt:        generation.Attempt,
		ProviderCallID: generation.ProviderCallID,
		ProviderEpoch:  service.config.ProviderEpoch,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("restart generation: %w", err))
	}

	return restarted, nil
}

// requeue records the failed attempt's usage and authorizes a fresh call, together.
func (service *GenerationCheck) requeue(
	ctx context.Context, generation *dao.Generation, call *lib.ProviderCall,
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

		return service.recordUsage(ctx, generation, call)
	})
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return requeued, nil
}

// settle records the outcome and what it consumed, together.
//
// One transaction, because a terminal transition without its usage row is a charge nothing accounts
// for. The provider call happened before and outside it: a transaction held open across a call
// would pin a pooled connection for its whole duration.
func (service *GenerationCheck) settle(
	ctx context.Context, generation *dao.Generation, call *lib.ProviderCall,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(settle)")
	defer span.End()

	status, failure, message := settleOutcomeOf(call)

	span.SetAttributes(attribute.String("generation.status", string(status)))

	if status == dao.GenerationStatusFailed {
		service.logFailure(ctx, generation, call.Reason)
	}

	var settled *dao.Generation

	err := service.transactor.WithinTx(ctx, func(ctx context.Context) error {
		var err error

		settled, err = service.daos.Settle.Exec(ctx, &dao.GenerationSettleRequest{
			ID:             generation.ID,
			Attempt:        generation.Attempt,
			ProviderCallID: generation.ProviderCallID,
			Status:         status,
			Output:         call.Output,
			Failure:        failure,
			Error:          message,
			Retention:      service.config.Retention,
		})
		if err != nil {
			return fmt.Errorf("settle generation: %w", err)
		}

		return service.recordUsage(ctx, generation, call)
	})
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	return settled, nil
}

// settleFailure records a terminal failure with nothing to account for, without exposing provider
// details to callers.
func (service *GenerationCheck) settleFailure(
	ctx context.Context, generation *dao.Generation, failure lib.Failure,
) (*dao.Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(settleFailure)")
	defer span.End()

	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
	defer cancel()

	settled, err := service.daos.Settle.Exec(settleCtx, &dao.GenerationSettleRequest{
		ID:             generation.ID,
		Attempt:        generation.Attempt,
		ProviderCallID: generation.ProviderCallID,
		Status:         dao.GenerationStatusFailed,
		Failure:        nonEmpty(string(failure.Kind)),
		Error:          &failure.Message,
		Retention:      service.config.Retention,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("settle failed generation: %w", err))
	}

	return settled, nil
}

func (service *GenerationCheck) recordUsage(
	ctx context.Context, generation *dao.Generation, call *lib.ProviderCall,
) error {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(recordUsage)")
	defer span.End()

	// Absent when the operation never reached the model: there is nothing to account for.
	if call.Usage == nil {
		return nil
	}

	_, err := service.daos.Usage.Exec(ctx, &dao.GenerationUsageInsertRequest{
		GenerationID:      generation.ID,
		Attempt:           generation.Attempt,
		OwnerID:           generation.OwnerID,
		Purpose:           generation.Purpose,
		Provider:          service.config.ProviderName,
		Model:             call.Model,
		ReasoningEffort:   nonEmpty(call.ReasoningEffort),
		InputTokens:       call.Usage.InputTokens,
		CachedInputTokens: call.Usage.CachedInputTokens,
		CacheWriteTokens:  call.Usage.CacheWriteTokens,
		OutputTokens:      call.Usage.OutputTokens,
		ReasoningTokens:   call.Usage.ReasoningTokens,
	})

	// A replay of the same provider result finds the row already there. That is the idempotent
	// outcome, not a failure.
	if err != nil && !errors.Is(err, dao.ErrGenerationUsageExists) {
		return otel.ReportError(span, fmt.Errorf("record usage: %w", err))
	}

	return nil
}

// stopOrphan cancels an accepted call whose generation settled first. Best effort: a failure is
// logged, and the call runs to completion unread.
func (service *GenerationCheck) stopOrphan(ctx context.Context, generation *dao.Generation, callID string) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationCheck(stopOrphan)")
	defer span.End()

	cancelCtx, cancel := context.WithTimeout(ctx, persistenceTimeout)
	defer cancel()

	_, err := service.provider.Cancel(cancelCtx, callID)
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

// settleOutcomeOf maps a terminal provider state onto the status to land in, with the failure kind
// and the message a caller reads.
//
// A refused or incomplete call is a failure of the generation but not a free one: it consumed
// tokens, which is why settle still writes usage for it.
func settleOutcomeOf(call *lib.ProviderCall) (dao.GenerationStatus, *string, *string) {
	switch call.State {
	case lib.ProviderCallSucceeded:
		return dao.GenerationStatusSucceeded, nil, nil
	case lib.ProviderCallCancelled:
		return dao.GenerationStatusCancelled, nil, nonEmpty(generationCancelledReason)
	case lib.ProviderCallFailed:
		failure := failureCallFailed
		if call.Failure != nil {
			failure = *call.Failure
		}

		return dao.GenerationStatusFailed, nonEmpty(string(failure.Kind)), nonEmpty(failure.Message)
	case lib.ProviderCallRunning:
		fallthrough
	default:
		// Unreachable: observe only settles a terminal state. Failing rather than panicking keeps a
		// provider that grows a new status from stranding the generation.
		return dao.GenerationStatusFailed, nonEmpty(string(failureCallFailed.Kind)), nonEmpty(failureCallFailed.Message)
	}
}

func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}

	return &value
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

	generationCancelledReason = "generation cancelled"
)

// Failures this service names itself. A provider's own failures carry their messages from the
// adapter.
var (
	failureOutcomeUnknown = lib.Failure{
		Kind: lib.FailureFailed, Message: "the provider may have accepted the call, but its outcome is unknown",
	}
	failureResultLost = lib.Failure{
		Kind: lib.FailureFailed, Message: "the provider no longer holds the result",
	}
	failureStartFailed = lib.Failure{
		Kind: lib.FailureFailed, Message: "the provider call could not be started",
	}
	failureCallFailed = lib.Failure{
		Kind: lib.FailureFailed, Message: "the provider call failed",
	}
	failureUnreadableRequest = lib.Failure{
		Kind: lib.FailureFailed, Message: "the stored request is unreadable",
	}
)
