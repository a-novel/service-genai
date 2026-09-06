package core_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
	libmocks "github.com/a-novel/service-genai/internal/lib/mocks"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

type claimHook struct {
	after func(context.Context, *dao.Generation)
}

func (hook *claimHook) Exec(ctx context.Context, request *dao.GenerationClaimRequest) ([]*dao.Generation, error) {
	claimed, err := dao.NewGenerationClaim().Exec(ctx, request)
	if err == nil && len(claimed) > 0 {
		hook.after(ctx, claimed[0])
	}

	return claimed, err
}

type controlHook struct {
	after func(*dao.Generation, error)
}

func (hook *controlHook) Exec(ctx context.Context, request *dao.GenerationControlRequest) (*dao.Generation, error) {
	generation, err := dao.NewGenerationControl().Exec(ctx, request)
	hook.after(generation, err)

	return generation, err
}

func workerDaos() core.WorkerDaos {
	return core.WorkerDaos{
		Claim: dao.NewGenerationClaim(), Control: dao.NewGenerationControl(), BeginStart: dao.NewGenerationBeginStart(),
		Record: dao.NewGenerationRecordProviderCall(), Settle: dao.NewGenerationSettle(),
		ObserveLater: dao.NewGenerationObserveLater(),
		Requeue:      dao.NewGenerationRequeue(),
		Usage:        dao.NewGenerationUsageInsert(),
	}
}

func seedWorkerGeneration(ctx context.Context, t *testing.T) *dao.Generation {
	t.Helper()

	submitted, err := dao.NewGenerationSubmit().Exec(ctx, &dao.GenerationSubmitRequest{
		ID: uuid.New(), OwnerID: uuid.New(), Purpose: "studio.generation", IdempotencyKey: uuid.NewString(),
		RequestFingerprint: []byte{1}, Request: json.RawMessage(`{"model":"test"}`), MaxAttempts: 3,
	})
	require.NoError(t, err)

	return submitted.Generation
}

func readWorkerGeneration(ctx context.Context, t *testing.T, generation *dao.Generation) *dao.Generation {
	t.Helper()

	current, err := dao.NewGenerationGet().Exec(ctx, &dao.GenerationGetRequest{
		ID:      generation.ID,
		OwnerID: generation.OwnerID,
	})
	require.NoError(t, err)

	return current
}

func cancelWorkerGeneration(ctx context.Context, t *testing.T, generation *dao.Generation) {
	t.Helper()

	_, err := dao.NewGenerationRequestCancel().Exec(ctx, &dao.GenerationRequestCancelRequest{
		ID:      generation.ID,
		OwnerID: generation.OwnerID,
	})
	require.NoError(t, err)
}

func newDatabaseWorker(
	t *testing.T, provider lib.Provider, daos core.WorkerDaos, config core.WorkerConfig,
) *core.Worker {
	t.Helper()

	worker, err := core.NewWorker(config, &recordingWorkerLogger{}, provider, postgres.NewTransactor(nil), daos)
	require.NoError(t, err)

	return worker
}

func waitWorkerResult(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop")

		return nil
	}
}

func TestWorkerCancellationAfterClaim(t *testing.T) {
	t.Parallel()
	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		generation := seedWorkerGeneration(ctx, t)
		provider := libmocks.NewMockProvider(t)
		daos := workerDaos()
		daos.Claim = &claimHook{after: func(ctx context.Context, claimed *dao.Generation) {
			require.Nil(t, claimed.CancelRequestedAt)
			cancelWorkerGeneration(ctx, t, generation)
		}}
		worker := newDatabaseWorker(t, provider, daos, testWorkerConfig())
		worked, err := worker.RunOnce(ctx)
		require.NoError(t, err)
		require.True(t, worked)
		current := readWorkerGeneration(ctx, t, generation)
		require.Equal(t, dao.GenerationStatusCancelled, current.Status)
		require.Nil(t, current.StartRequestedAt)
		require.Nil(t, current.ProviderCallID)
		provider.AssertNotCalled(t, "Start", mock.Anything, mock.Anything)
	})
}

func TestWorkerCancellationDuringStart(t *testing.T) {
	t.Parallel()
	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		generation := seedWorkerGeneration(ctx, t)
		provider := libmocks.NewMockProvider(t)
		started := make(chan struct{})
		release := make(chan struct{})

		provider.EXPECT().Start(mock.Anything, mock.Anything).
			RunAndReturn(func(ctx context.Context, _ *lib.ProviderStartRequest) (*lib.ProviderCall, error) {
				close(started)

				select {
				case <-release:
					return call(lib.ProviderCallRunning), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}).Once()

		cancelled := call(lib.ProviderCallCancelled)
		cancelled.Usage = &lib.ProviderUsage{InputTokens: 10, OutputTokens: 5}

		provider.EXPECT().Cancel(mock.Anything, testCallID).Return(call(lib.ProviderCallRunning), nil).Once()
		provider.EXPECT().Cancel(mock.Anything, testCallID).Return(cancelled, nil).Once()
		provider.EXPECT().Name().Return("openai").Once()
		worker := newDatabaseWorker(t, provider, workerDaos(), testWorkerConfig())

		workCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		done := make(chan error, 1)

		go func() { _, err := worker.RunOnce(workCtx); done <- err }()

		select {
		case <-started:
		case <-workCtx.Done():
			t.Fatal("provider did not start")
		}

		require.NotNil(t, readWorkerGeneration(ctx, t, generation).StartRequestedAt)
		cancelWorkerGeneration(ctx, t, generation)
		close(release)
		require.NoError(t, waitWorkerResult(t, done))
		current := readWorkerGeneration(ctx, t, generation)
		require.Equal(t, dao.GenerationStatusCancelled, current.Status)
		require.Equal(t, testCallID, *current.ProviderCallID)

		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)

		usage := []dao.GenerationUsage{}
		err = db.NewSelect().Model(&usage).Where("generation_id = ?", generation.ID).Scan(ctx)
		require.NoError(t, err)
		require.Len(t, usage, 1)
		require.Equal(t, int16(1), usage[0].Attempt)
		require.Equal(t, int64(10), usage[0].InputTokens)
	})
}

func TestWorkerRenewsWithoutIdleClaims(t *testing.T) {
	t.Parallel()
	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		first := seedWorkerGeneration(ctx, t)
		second := seedWorkerGeneration(ctx, t)
		provider := libmocks.NewMockProvider(t)
		entered := make(chan struct{})
		release := make(chan struct{})

		var blocked atomic.Bool

		provider.EXPECT().Start(mock.Anything, mock.Anything).
			RunAndReturn(func(ctx context.Context, _ *lib.ProviderStartRequest) (*lib.ProviderCall, error) {
				blocked.Store(true)
				close(entered)

				select {
				case <-release:
					return succeededCall(), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}).Once()
		provider.EXPECT().Name().Return("openai").Once()

		renewals := make(chan *dao.Generation, 8)
		daos := workerDaos()
		daos.Control = &controlHook{after: func(generation *dao.Generation, err error) {
			if err == nil && blocked.Load() {
				select {
				case renewals <- generation:
				default:
				}
			}
		}}
		config := testWorkerConfig()
		config.Lease = 3 * time.Second
		config.BatchSize = 10
		worker := newDatabaseWorker(t, provider, daos, config)

		workCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		done := make(chan error, 1)

		go func() { _, err := worker.RunOnce(workCtx); done <- err }()

		select {
		case <-entered:
		case <-workCtx.Done():
			t.Fatal("provider did not start")
		}

		initial := readWorkerGeneration(ctx, t, first)
		require.Equal(t, dao.GenerationStatusPending, readWorkerGeneration(ctx, t, second).Status)
		other, err := dao.NewGenerationClaim().Exec(ctx, &dao.GenerationClaimRequest{
			WorkerID: "worker-2",
			Limit:    1,
			Lease:    time.Minute,
		})
		require.NoError(t, err)
		require.Len(t, other, 1)
		require.Equal(t, second.ID, other[0].ID)

		for range 4 {
			select {
			case renewed := <-renewals:
				require.Equal(t, initial.ClaimToken, renewed.ClaimToken)
			case <-workCtx.Done():
				t.Fatal("claim was not renewed while provider blocked")
			}
		}

		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)

		var pastOriginalLease bool

		err = db.NewRaw("SELECT clock_timestamp() > ?", initial.LeaseExpiresAt).Scan(ctx, &pastOriginalLease)
		require.NoError(t, err)
		require.True(t, pastOriginalLease)

		reaped, err := dao.NewGenerationReap().Exec(ctx, &dao.GenerationReapRequest{
			Limit:     10,
			Retention: time.Hour,
		})
		require.NoError(t, err)
		require.Empty(t, reaped)
		close(release)
		require.NoError(t, waitWorkerResult(t, done))
		require.Equal(t, dao.GenerationStatusSucceeded, readWorkerGeneration(ctx, t, first).Status)
	})
}

func TestWorkerLostLeaseDuringStartPreservesUncertainty(t *testing.T) {
	t.Parallel()
	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		generation := seedWorkerGeneration(ctx, t)
		started := make(chan struct{})
		provider := libmocks.NewMockProvider(t)
		provider.EXPECT().Start(mock.Anything, mock.Anything).
			RunAndReturn(func(ctx context.Context, _ *lib.ProviderStartRequest) (*lib.ProviderCall, error) {
				close(started)
				<-ctx.Done()
				// Acceptance can race with cancellation of the transport.
				return call(lib.ProviderCallRunning), nil
			}).Once()

		config := testWorkerConfig()
		config.Lease = 3 * time.Second
		worker := newDatabaseWorker(t, provider, workerDaos(), config)

		workCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		done := make(chan error, 1)

		go func() { _, err := worker.RunOnce(workCtx); done <- err }()

		select {
		case <-started:
		case <-workCtx.Done():
			t.Fatal("provider did not start")
		}

		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)
		_, err = db.NewUpdate().Model((*dao.Generation)(nil)).
			Set("lease_expires_at = clock_timestamp() - interval '1 second'").
			Where("id = ?", generation.ID).Exec(ctx)
		require.NoError(t, err)
		require.NoError(t, waitWorkerResult(t, done))
		current := readWorkerGeneration(ctx, t, generation)
		require.Nil(t, current.ProviderCallID)
		require.Equal(t, dao.GenerationStatusRunning, current.Status)

		reaped, err := dao.NewGenerationReap().Exec(ctx, &dao.GenerationReapRequest{
			Limit:     1,
			Retention: time.Hour,
		})
		require.NoError(t, err)
		require.Len(t, reaped, 1)
		require.Equal(t, dao.GenerationStatusFailed, reaped[0].Status)
		require.Equal(t, "generation outcome unknown", *reaped[0].Error)

		worked, err := worker.RunOnce(ctx)
		require.NoError(t, err)
		require.False(t, worked)
		provider.AssertNotCalled(t, "Get", mock.Anything, mock.Anything)
	})
}

func TestWorkerUsageRollsBackWhenClaimLost(t *testing.T) {
	t.Parallel()
	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		generation := seedWorkerGeneration(ctx, t)
		provider := libmocks.NewMockProvider(t)
		provider.EXPECT().Start(mock.Anything, mock.Anything).Return(call(lib.ProviderCallRunning), nil).Once()
		failed := call(lib.ProviderCallFailed)
		failed.Retryable = true
		failed.Usage = &lib.ProviderUsage{InputTokens: 10, OutputTokens: 5}

		provider.EXPECT().Get(mock.Anything, testCallID).
			RunAndReturn(func(_ context.Context, _ string) (*lib.ProviderCall, error) {
				current := readWorkerGeneration(ctx, t, generation)
				_, err := dao.NewGenerationObserveLater().Exec(ctx, &dao.GenerationObserveLaterRequest{
					ID:         current.ID,
					WorkerID:   testWorkerID,
					ClaimToken: current.ClaimToken,
				})
				require.NoError(t, err)
				claimed, err := dao.NewGenerationClaim().Exec(ctx, &dao.GenerationClaimRequest{
					WorkerID: testWorkerID,
					Limit:    1,
					Lease:    time.Minute,
				})
				require.NoError(t, err)
				require.Len(t, claimed, 1)
				require.Equal(t, current.Attempt, claimed[0].Attempt)
				require.NotEqual(t, current.ClaimToken, claimed[0].ClaimToken)

				return failed, nil
			}).Once()
		provider.EXPECT().Name().Return("openai").Once()
		worker := newDatabaseWorker(t, provider, workerDaos(), testWorkerConfig())
		worked, err := worker.RunOnce(ctx)
		require.NoError(t, err)
		require.True(t, worked)
		current := readWorkerGeneration(ctx, t, generation)
		require.Equal(t, dao.GenerationStatusRunning, current.Status)
		require.Equal(t, testCallID, *current.ProviderCallID)

		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)
		count, err := db.NewSelect().Model((*dao.GenerationUsage)(nil)).Where("generation_id = ?", generation.ID).Count(ctx)
		require.NoError(t, err)
		require.Zero(t, count)
	})
}
