package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/transaction"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
)

// Dependencies of [Worker].
type (
	// WorkerLogger records failures whose provider details must remain server-side.
	WorkerLogger interface {
		Err(ctx context.Context, msg string, fields ...any)
	}
	// WorkerClaimDao takes a batch of pending generations.
	WorkerClaimDao interface {
		Exec(ctx context.Context, request *dao.GenerationClaimRequest) ([]*dao.Generation, error)
	}
	// WorkerRecordProviderCallDao attaches a provider operation to a running generation.
	WorkerRecordProviderCallDao interface {
		Exec(ctx context.Context, request *dao.GenerationRecordProviderCallRequest) (*dao.Generation, error)
	}
	// WorkerSettleDao records a terminal outcome.
	WorkerSettleDao interface {
		Exec(ctx context.Context, request *dao.GenerationSettleRequest) (*dao.Generation, error)
	}
	// WorkerObserveLaterDao schedules another observation of a known provider operation.
	WorkerObserveLaterDao interface {
		Exec(ctx context.Context, request *dao.GenerationObserveLaterRequest) (*dao.Generation, error)
	}
	// WorkerRequeueDao authorizes a fresh inference attempt after a definitive retryable failure.
	WorkerRequeueDao interface {
		Exec(ctx context.Context, request *dao.GenerationRequeueRequest) (*dao.Generation, error)
	}
	// WorkerUsageInsertDao records what one attempt consumed.
	WorkerUsageInsertDao interface {
		Exec(ctx context.Context, request *dao.GenerationUsageInsertRequest) (*dao.GenerationUsage, error)
	}
)

// WorkerDaos are the data-access dependencies of a [Worker]. Grouping them prevents positional
// swaps between interfaces that all take a request and return a generation.
type WorkerDaos struct {
	Claim        WorkerClaimDao
	Record       WorkerRecordProviderCallDao
	Settle       WorkerSettleDao
	ObserveLater WorkerObserveLaterDao
	Requeue      WorkerRequeueDao
	Usage        WorkerUsageInsertDao
}

// WorkerConfig is what a [Worker] needs to run.
type WorkerConfig struct {
	// ID identifies this worker on the claims it holds, so a stranded one can be traced.
	ID string `validate:"required,notblank"`
	// Lease is how long a claim holds. Size it to the expected run, not a multiple of it: an outrun
	// lease is recoverable, and safety comes from MaxAttempts plus the recorded provider call.
	Lease time.Duration `validate:"required"`
	// BatchSize caps one claim.
	BatchSize int `validate:"required,min=1,max=100"`
	// PollInterval is how long the provider is given between polls of a running operation.
	PollInterval time.Duration `validate:"required"`
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration `validate:"required"`
}

// A Worker runs submitted generations.
//
// It is what used to live in every consumer. The loop is small; what matters is the order of its
// steps, because the window between starting a provider call and recording its identifier is the
// only place a crash can cost money.
type Worker struct {
	config WorkerConfig

	logger     WorkerLogger
	provider   lib.Provider
	transactor transaction.Transactor
	daos       WorkerDaos
}

func NewWorker(
	config WorkerConfig,
	logger WorkerLogger,
	provider lib.Provider,
	transactor transaction.Transactor,
	daos WorkerDaos,
) (*Worker, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	if config.Lease > ClaimLeaseCeiling {
		return nil, fmt.Errorf("%w: lease %s exceeds %s", ErrInvalidRequest, config.Lease, ClaimLeaseCeiling)
	}

	if logger == nil {
		return nil, fmt.Errorf("%w: logger is required", ErrInvalidRequest)
	}

	return &Worker{
		config: config, logger: logger, provider: provider, transactor: transactor, daos: daos,
	}, nil
}

// RunOnce claims a batch and runs it to completion, reporting whether it found work. It is the
// function the poll loop drives.
func (worker *Worker) RunOnce(ctx context.Context) (bool, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.RunOnce")
	defer span.End()

	claimed, err := worker.daos.Claim.Exec(ctx, &dao.GenerationClaimRequest{
		WorkerID: worker.config.ID,
		Limit:    worker.config.BatchSize,
		Lease:    worker.config.Lease,
	})
	if err != nil {
		return false, otel.ReportError(span, fmt.Errorf("claim generations: %w", err))
	}

	span.SetAttributes(attribute.Int("worker.claimed", len(claimed)))

	for _, generation := range claimed {
		// One generation's failure is recorded on that generation and must not abandon the rest of
		// the batch, which is already claimed and would sit until its lease lapsed.
		runErr := worker.run(ctx, generation)
		if runErr != nil {
			_ = otel.ReportError(span, fmt.Errorf("run generation %s: %w", generation.ID, runErr))
		}
	}

	return otel.ReportSuccess(span, len(claimed) > 0), nil
}

// run takes one claimed generation to a terminal state, or back to the queue.
func (worker *Worker) run(ctx context.Context, generation *dao.Generation) error {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.run")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.id", generation.ID.String()),
		attribute.Int("generation.attempt", int(generation.Attempt)),
		attribute.Bool("generation.resumed", generation.ProviderCallID != nil),
	)

	var (
		call *lib.ProviderCall
		err  error
	)

	if generation.ProviderCallID == nil {
		call, err = worker.startInference(ctx, generation)
		if err != nil {
			if errors.Is(err, errProviderOperationPersistence) {
				return otel.ReportError(span, worker.failPersistence(ctx, generation, err))
			}

			return otel.ReportError(span, worker.failInference(ctx, generation, err))
		}
	} else {
		call, err = worker.resumeProviderOperation(ctx, *generation.ProviderCallID)
		if err != nil {
			return otel.ReportError(span, worker.failObservation(ctx, generation, err))
		}
	}

	call, err = worker.await(ctx, generation, call)
	if err != nil {
		return otel.ReportError(span, worker.failObservation(ctx, generation, err))
	}

	return otel.ReportSuccess(span, worker.finishProviderOperation(ctx, generation, call))
}

// startInference begins paid work and records its provider operation before polling it.
func (worker *Worker) startInference(
	ctx context.Context,
	generation *dao.Generation,
) (*lib.ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.startInference")
	defer span.End()

	call, err := worker.provider.Start(ctx, &lib.ProviderStartRequest{
		Request:      generation.Request,
		GenerationID: generation.ID.String(),
		Attempt:      generation.Attempt,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("start provider call: %w", err))
	}

	_, err = worker.daos.Record.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
		ID:             generation.ID,
		WorkerID:       worker.config.ID,
		ProviderCallID: call.ID,
	})
	if err != nil {
		return nil, otel.ReportError(
			span,
			fmt.Errorf("%w: record provider call: %w", errProviderOperationPersistence, err),
		)
	}

	generation.ProviderCallID = &call.ID

	return otel.ReportSuccess(span, call), nil
}

// resumeProviderOperation observes paid work whose provider identifier is already durable.
func (worker *Worker) resumeProviderOperation(ctx context.Context, id string) (*lib.ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.resumeProviderOperation")
	defer span.End()

	call, err := worker.observeProviderOperation(ctx, id)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("re-attach to provider operation: %w", err))
	}

	return otel.ReportSuccess(span, call), nil
}

// await polls until the operation is terminal, stopping early if a cancel was requested.
//
// The lease bounds this: a poll loop that outran its lease has already had the generation recovered
// underneath it, and its settle will be refused.
func (worker *Worker) await(
	ctx context.Context, generation *dao.Generation, call *lib.ProviderCall,
) (*lib.ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.await")
	defer span.End()

	for !call.State.Terminal() {
		if generation.CancelRequestedAt != nil {
			cancelled, err := worker.provider.Cancel(ctx, call.ID)
			if err != nil {
				return nil, otel.ReportError(span, fmt.Errorf("cancel provider call: %w", err))
			}

			return otel.ReportSuccess(span, cancelled), nil
		}

		err := waitForContext(ctx, worker.config.PollInterval)
		if err != nil {
			return nil, otel.ReportError(span, err)
		}

		polled, err := worker.observeProviderOperation(ctx, call.ID)
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("poll provider operation: %w", err))
		}

		call = polled
	}

	span.SetAttributes(attribute.String("provider.state", string(call.State)))

	return otel.ReportSuccess(span, call), nil
}

// observeProviderOperation retries transient observation failures within one bounded claim budget.
func (worker *Worker) observeProviderOperation(ctx context.Context, id string) (*lib.ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.observeProviderOperation")
	defer span.End()

	for attempt := 1; attempt <= providerObservationAttempts; attempt++ {
		span.SetAttributes(attribute.Int("observation.attempt", attempt))

		call, err := worker.provider.Get(ctx, id)
		if err == nil {
			return otel.ReportSuccess(span, call), nil
		}

		if !errors.Is(err, lib.ErrProviderRetryable) {
			return nil, otel.ReportError(span, fmt.Errorf("get provider operation: %w", err))
		}

		if attempt == providerObservationAttempts {
			return nil, otel.ReportError(
				span,
				fmt.Errorf("observation budget exhausted after %d attempts: %w", attempt, err),
			)
		}

		err = waitForContext(ctx, worker.observationBackoff(attempt))
		if err != nil {
			return nil, otel.ReportError(span, err)
		}
	}

	panic("unreachable provider observation loop")
}

// finishProviderOperation applies the explicit fresh-attempt policy to a terminal provider result.
func (worker *Worker) finishProviderOperation(
	ctx context.Context,
	generation *dao.Generation,
	call *lib.ProviderCall,
) error {
	if call.State == lib.ProviderCallFailed && call.Retryable && generation.Attempt < generation.MaxAttempts {
		return worker.retryTerminalInferenceFailure(ctx, generation, call)
	}

	return worker.settle(ctx, generation, call)
}

// retryTerminalInferenceFailure accounts for the completed attempt before authorizing another one.
func (worker *Worker) retryTerminalInferenceFailure(
	ctx context.Context,
	generation *dao.Generation,
	call *lib.ProviderCall,
) error {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.retryTerminalInferenceFailure")
	defer span.End()

	worker.logFailure(ctx, generation, call.Reason)

	return otel.ReportSuccess(span, worker.transactor.WithinTx(ctx, func(ctx context.Context) error {
		err := worker.recordUsage(ctx, generation, call)
		if err != nil {
			return err
		}

		_, err = worker.daos.Requeue.Exec(ctx, &dao.GenerationRequeueRequest{
			ID: generation.ID, WorkerID: worker.config.ID, ProviderCallID: generation.ProviderCallID,
		})
		if err != nil {
			return fmt.Errorf("requeue generation after terminal provider failure: %w", err)
		}

		return nil
	}))
}

// settle records the outcome and what it consumed, together.
//
// The two writes are one transaction on purpose: a terminal transition without its usage row is a
// charge nothing accounts for. The provider call happened before this and outside it, because a
// transaction held open across a call that takes minutes would pin a pooled connection and block
// reclamation of dead rows for its whole duration.
func (worker *Worker) settle(ctx context.Context, generation *dao.Generation, call *lib.ProviderCall) error {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.settle")
	defer span.End()

	status, reason := settleOutcomeOf(call)

	span.SetAttributes(
		attribute.String("generation.id", generation.ID.String()),
		attribute.String("generation.status", string(status)),
	)

	if status == dao.GenerationStatusFailed {
		worker.logFailure(ctx, generation, call.Reason)
	}

	return otel.ReportSuccess(span, worker.transactor.WithinTx(ctx, func(ctx context.Context) error {
		_, err := worker.daos.Settle.Exec(ctx, &dao.GenerationSettleRequest{
			ID:        generation.ID,
			WorkerID:  worker.config.ID,
			Status:    status,
			Output:    call.Output,
			Error:     reason,
			Retention: worker.config.Retention,
		})
		if err != nil {
			return fmt.Errorf("settle generation: %w", err)
		}

		return worker.recordUsage(ctx, generation, call)
	}))
}

func (worker *Worker) recordUsage(
	ctx context.Context,
	generation *dao.Generation,
	call *lib.ProviderCall,
) error {
	// Absent when the operation never reached the model, which is the one case with nothing to
	// account for.
	if call.Usage == nil {
		return nil
	}

	_, err := worker.daos.Usage.Exec(ctx, &dao.GenerationUsageInsertRequest{
		GenerationID:      generation.ID,
		Attempt:           generation.Attempt,
		OwnerID:           generation.OwnerID,
		Purpose:           generation.Purpose,
		Provider:          worker.provider.Name(),
		Model:             call.Model,
		InputTokens:       call.Usage.InputTokens,
		CachedInputTokens: call.Usage.CachedInputTokens,
		OutputTokens:      call.Usage.OutputTokens,
		ReasoningTokens:   call.Usage.ReasoningTokens,
	})

	// A replay of the same provider result finds the row already there. That is the idempotent
	// outcome, not a failure.
	if err != nil && !errors.Is(err, dao.ErrGenerationUsageExists) {
		return fmt.Errorf("record usage: %w", err)
	}

	return nil
}

// failInference applies the only policy that may authorize another paid provider operation.
func (worker *Worker) failInference(ctx context.Context, generation *dao.Generation, cause error) error {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.failInference")
	defer span.End()

	if errors.Is(cause, lib.ErrProviderStartAmbiguous) {
		return otel.ReportSuccess(
			span,
			worker.settleFailure(ctx, generation, cause, generationOutcomeUnknownReason),
		)
	}

	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		span.SetAttributes(attribute.Bool("failure.shutdown", true))

		return otel.ReportSuccess(span, cause)
	}

	retryable := errors.Is(cause, lib.ErrProviderRetryable)
	hasAttemptsLeft := generation.Attempt < generation.MaxAttempts

	span.SetAttributes(
		attribute.String("generation.id", generation.ID.String()),
		attribute.Bool("failure.retryable", retryable),
		attribute.Bool("failure.attempts_left", hasAttemptsLeft),
	)

	if retryable && hasAttemptsLeft {
		_, err := worker.daos.Requeue.Exec(ctx, &dao.GenerationRequeueRequest{
			ID: generation.ID, WorkerID: worker.config.ID,
		})
		if err != nil {
			return otel.ReportError(span, fmt.Errorf("requeue generation: %w", err))
		}

		return otel.ReportSuccess(span, cause)
	}

	return otel.ReportSuccess(span, worker.settleFailure(ctx, generation, cause, generationFailedReason))
}

// failObservation retains known provider work after a transient lifecycle failure.
func (worker *Worker) failObservation(
	ctx context.Context,
	generation *dao.Generation,
	cause error,
) error {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.failObservation")
	defer span.End()

	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		span.SetAttributes(attribute.Bool("failure.shutdown", true))

		return otel.ReportSuccess(span, cause)
	}

	if errors.Is(cause, lib.ErrProviderRetryable) {
		_, err := worker.daos.ObserveLater.Exec(ctx, &dao.GenerationObserveLaterRequest{
			ID:         generation.ID,
			WorkerID:   worker.config.ID,
			RetryAfter: worker.observationBackoff(providerObservationAttempts),
		})
		if err != nil {
			return otel.ReportError(span, fmt.Errorf("schedule provider observation: %w", err))
		}

		return otel.ReportSuccess(span, cause)
	}

	return otel.ReportSuccess(span, worker.settleFailure(ctx, generation, cause, generationFailedReason))
}

// failPersistence preserves uncertainty when a provider operation could not be made durable.
func (worker *Worker) failPersistence(ctx context.Context, generation *dao.Generation, cause error) error {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.failPersistence")
	defer span.End()

	return otel.ReportSuccess(
		span,
		worker.settleFailure(ctx, generation, cause, generationOutcomeUnknownReason),
	)
}

// settleFailure records a terminal local decision without exposing provider details to clients.
func (worker *Worker) settleFailure(
	ctx context.Context,
	generation *dao.Generation,
	cause error,
	reason string,
) error {
	worker.logFailure(ctx, generation, cause.Error())

	_, err := worker.daos.Settle.Exec(ctx, &dao.GenerationSettleRequest{
		ID:        generation.ID,
		WorkerID:  worker.config.ID,
		Status:    dao.GenerationStatusFailed,
		Error:     &reason,
		Retention: worker.config.Retention,
	})
	if err != nil {
		return fmt.Errorf("settle failed generation: %w", err)
	}

	return cause
}

func (worker *Worker) observationBackoff(attempt int) time.Duration {
	return worker.config.PollInterval * time.Duration(1<<(attempt-1))
}

func waitForContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// settleOutcomeOf maps a terminal provider state onto the status to land in.
//
// An incomplete response and a refusal are failures of the generation but not of the call: they
// consumed tokens, which is why settle still writes usage for them.
func settleOutcomeOf(call *lib.ProviderCall) (dao.GenerationStatus, *string) {
	switch call.State {
	case lib.ProviderCallSucceeded:
		return dao.GenerationStatusSucceeded, nil
	case lib.ProviderCallCancelled:
		return dao.GenerationStatusCancelled, nonEmpty(generationCancelledReason)
	case lib.ProviderCallIncomplete, lib.ProviderCallFailed:
		return dao.GenerationStatusFailed, nonEmpty(generationFailedReason)
	case lib.ProviderCallRunning:
		fallthrough
	default:
		// Unreachable: await only returns on a terminal state. Settling as failed rather than
		// panicking keeps a provider that grows a new status from stranding the generation.
		return dao.GenerationStatusFailed, nonEmpty(generationFailedReason)
	}
}

var errProviderOperationPersistence = errors.New("provider operation persistence failed")

const (
	providerObservationAttempts    = 3
	generationCancelledReason      = "generation cancelled"
	generationFailedReason         = "generation failed"
	generationOutcomeUnknownReason = "generation outcome unknown"
)

func (worker *Worker) logFailure(
	ctx context.Context,
	generation *dao.Generation,
	reason string,
) {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.logFailure")
	defer span.End()

	if reason == "" {
		reason = "provider returned no failure reason"
	}

	worker.logger.Err(
		ctx,
		fmt.Sprintf("generation %s failed: %s", generation.ID, reason),
	)
}

func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}

	return &value
}
