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

// Data-access dependencies of [Worker].
type (
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
	// WorkerRequeueDao returns a generation to the queue after a retryable failure.
	WorkerRequeueDao interface {
		Exec(ctx context.Context, request *dao.GenerationRequeueRequest) (*dao.Generation, error)
	}
	// WorkerUsageInsertDao records what one attempt consumed.
	WorkerUsageInsertDao interface {
		Exec(ctx context.Context, request *dao.GenerationUsageInsertRequest) (*dao.GenerationUsage, error)
	}
)

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

	provider   lib.Provider
	transactor transaction.Transactor

	claimDao   WorkerClaimDao
	recordDao  WorkerRecordProviderCallDao
	settleDao  WorkerSettleDao
	requeueDao WorkerRequeueDao
	usageDao   WorkerUsageInsertDao
}

func NewWorker(
	config WorkerConfig,
	provider lib.Provider,
	transactor transaction.Transactor,
	claimDao WorkerClaimDao,
	recordDao WorkerRecordProviderCallDao,
	settleDao WorkerSettleDao,
	requeueDao WorkerRequeueDao,
	usageDao WorkerUsageInsertDao,
) (*Worker, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	if config.Lease > ClaimLeaseCeiling {
		return nil, fmt.Errorf("%w: lease %s exceeds %s", ErrInvalidRequest, config.Lease, ClaimLeaseCeiling)
	}

	return &Worker{
		config: config, provider: provider, transactor: transactor,
		claimDao: claimDao, recordDao: recordDao, settleDao: settleDao,
		requeueDao: requeueDao, usageDao: usageDao,
	}, nil
}

// RunOnce claims a batch and runs it to completion, reporting whether it found work. It is the
// function the poll loop drives.
func (worker *Worker) RunOnce(ctx context.Context) (bool, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.RunOnce")
	defer span.End()

	claimed, err := worker.claimDao.Exec(ctx, &dao.GenerationClaimRequest{
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

	call, err := worker.start(ctx, generation)
	if err != nil {
		return otel.ReportError(span, worker.fail(ctx, generation, err))
	}

	call, err = worker.await(ctx, generation, call)
	if err != nil {
		return otel.ReportError(span, worker.fail(ctx, generation, err))
	}

	return otel.ReportSuccess(span, worker.settle(ctx, generation, call))
}

// start re-attaches to the operation this generation already paid for, or begins a new one.
//
// The recorded identifier is what makes the difference. A generation carrying one is resumed, so a
// crash costs a poll rather than a second generation. A new call records its identifier immediately
// and before anything else — the gap between the provider accepting the call and that write landing
// is the only window in which a crash orphans a paid operation, and it cannot be closed because the
// provider offers no idempotency key on inference.
func (worker *Worker) start(ctx context.Context, generation *dao.Generation) (*lib.ProviderCall, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.start")
	defer span.End()

	if generation.ProviderCallID != nil {
		call, err := worker.provider.Get(ctx, *generation.ProviderCallID)
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("re-attach to provider call: %w", err))
		}

		return otel.ReportSuccess(span, call), nil
	}

	call, err := worker.provider.Start(ctx, &lib.ProviderStartRequest{
		Request:      generation.Request,
		GenerationID: generation.ID.String(),
		Attempt:      generation.Attempt,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("start provider call: %w", err))
	}

	_, err = worker.recordDao.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
		ID:             generation.ID,
		WorkerID:       worker.config.ID,
		ProviderCallID: call.ID,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("record provider call: %w", err))
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

		select {
		case <-ctx.Done():
			// Shutting down. The lease lapses, the reaper recovers the generation with its provider
			// call preserved, and the next claim re-attaches. Nothing is paid twice.
			return nil, otel.ReportError(span, ctx.Err())
		case <-time.After(worker.config.PollInterval):
		}

		polled, err := worker.provider.Get(ctx, call.ID)
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("poll provider call: %w", err))
		}

		call = polled
	}

	span.SetAttributes(attribute.String("provider.state", string(call.State)))

	return otel.ReportSuccess(span, call), nil
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

	return otel.ReportSuccess(span, worker.transactor.WithinTx(ctx, func(ctx context.Context) error {
		_, err := worker.settleDao.Exec(ctx, &dao.GenerationSettleRequest{
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

		// Absent when the operation never reached the model, which is the one case with nothing to
		// account for.
		if call.Usage == nil {
			return nil
		}

		_, err = worker.usageDao.Exec(ctx, &dao.GenerationUsageInsertRequest{
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

		// A replay of the same settle finds the row already there. That is the idempotent outcome,
		// not a failure.
		if err != nil && !errors.Is(err, dao.ErrGenerationUsageExists) {
			return fmt.Errorf("record usage: %w", err)
		}

		return nil
	}))
}

// fail decides whether a run that did not reach a terminal provider state is worth another attempt.
//
// A retryable failure with attempts left goes back to the queue, which clears the provider call so
// the next run starts a fresh one. Anything else settles as failed. A retryable failure with no
// attempt left settles too: there is nothing left to spend.
func (worker *Worker) fail(ctx context.Context, generation *dao.Generation, cause error) error {
	ctx, span := otel.Tracer().Start(ctx, "core.Worker.fail")
	defer span.End()

	retryable := errors.Is(cause, lib.ErrProviderRetryable)
	hasAttemptsLeft := generation.Attempt < generation.MaxAttempts

	span.SetAttributes(
		attribute.String("generation.id", generation.ID.String()),
		attribute.Bool("failure.retryable", retryable),
		attribute.Bool("failure.attempts_left", hasAttemptsLeft),
	)

	if retryable && hasAttemptsLeft {
		_, err := worker.requeueDao.Exec(ctx, &dao.GenerationRequeueRequest{
			ID: generation.ID, WorkerID: worker.config.ID,
		})
		if err != nil {
			return otel.ReportError(span, fmt.Errorf("requeue generation: %w", err))
		}

		return otel.ReportSuccess(span, cause)
	}

	reason := cause.Error()

	_, err := worker.settleDao.Exec(ctx, &dao.GenerationSettleRequest{
		ID:        generation.ID,
		WorkerID:  worker.config.ID,
		Status:    dao.GenerationStatusFailed,
		Error:     &reason,
		Retention: worker.config.Retention,
	})
	if err != nil {
		return otel.ReportError(span, fmt.Errorf("settle failed generation: %w", err))
	}

	return otel.ReportSuccess(span, cause)
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
		reason := call.Reason

		return dao.GenerationStatusCancelled, nonEmpty(reason)
	case lib.ProviderCallIncomplete, lib.ProviderCallFailed:
		return dao.GenerationStatusFailed, nonEmpty(call.Reason)
	case lib.ProviderCallRunning:
		fallthrough
	default:
		// Unreachable: await only returns on a terminal state. Settling as failed rather than
		// panicking keeps a provider that grows a new status from stranding the generation.
		return dao.GenerationStatusFailed, nonEmpty("provider reported a non-terminal state")
	}
}

func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}

	return &value
}
