package dao_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

const (
	testWorker    = "worker-1"
	testLease     = time.Minute
	testRetention = 7 * 24 * time.Hour
)

// seed submits one pending generation and returns it.
func seed(ctx context.Context, t *testing.T, maxAttempts int16) *dao.Generation {
	t.Helper()

	result, err := dao.NewGenerationSubmit().Exec(ctx, &dao.GenerationSubmitRequest{
		ID:                 uuid.Must(uuid.NewV7()),
		OwnerID:            uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Purpose:            "studio.generation",
		IdempotencyKey:     uuid.Must(uuid.NewV7()).String(),
		RequestFingerprint: []byte{0x01},
		Request:            json.RawMessage(`{"model": "a-model"}`),
		MaxAttempts:        maxAttempts,
	})
	require.NoError(t, err)

	return result.Generation
}

func claimOne(ctx context.Context, t *testing.T, worker string) []*dao.Generation {
	t.Helper()

	claimed, err := dao.NewGenerationClaim().Exec(ctx, &dao.GenerationClaimRequest{
		WorkerID: worker, Limit: 10, Lease: testLease,
	})
	require.NoError(t, err)

	return claimed
}

func TestGenerationClaim(t *testing.T) {
	t.Parallel()

	t.Run("Success", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			generation := seed(ctx, t, 1)

			claimed := claimOne(ctx, t, testWorker)
			require.Len(t, claimed, 1)
			require.Equal(t, generation.ID, claimed[0].ID)
			require.Equal(t, dao.GenerationStatusRunning, claimed[0].Status)
			require.Equal(t, int16(1), claimed[0].Attempt)
			require.NotNil(t, claimed[0].LeaseExpiresAt)
			require.Equal(t, testWorker, *claimed[0].ClaimedBy)
		})
	})

	t.Run("Success/ClaimedWorkIsNotClaimedTwice", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)

			require.Len(t, claimOne(ctx, t, testWorker), 1)
			require.Empty(t, claimOne(ctx, t, "worker-2"))
		})
	})

	t.Run("Error/OutsideCeilings", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			daoClaim := dao.NewGenerationClaim()

			for _, request := range []*dao.GenerationClaimRequest{
				{WorkerID: "", Limit: 1, Lease: testLease},
				{WorkerID: testWorker, Limit: 0, Lease: testLease},
				{WorkerID: testWorker, Limit: dao.GenerationClaimMaxLimit + 1, Lease: testLease},
				{WorkerID: testWorker, Limit: 1, Lease: 0},
				{WorkerID: testWorker, Limit: 1, Lease: dao.GenerationClaimMaxLease + time.Second},
			} {
				_, err := daoClaim.Exec(ctx, request)
				require.ErrorIs(t, err, dao.ErrGenerationClaimInvalid)
			}
		})
	})
}

func TestGenerationSettle(t *testing.T) {
	t.Parallel()

	t.Run("Success", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)
			claimed := claimOne(ctx, t, testWorker)

			settled, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
				ID:        claimed[0].ID,
				WorkerID:  testWorker,
				Status:    dao.GenerationStatusSucceeded,
				Output:    json.RawMessage(`{"text": "done"}`),
				Retention: testRetention,
			})
			require.NoError(t, err)
			require.Equal(t, dao.GenerationStatusSucceeded, settled.Status)
			require.NotNil(t, settled.SettledAt)
			// The purge reads expires_at, and the terminal-fields constraint refuses a settle
			// without it.
			require.NotNil(t, settled.ExpiresAt)
			require.Nil(t, settled.LeaseExpiresAt)
		})
	})

	t.Run("Error/NotHeldByThisWorker", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)
			claimed := claimOne(ctx, t, testWorker)

			_, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
				ID: claimed[0].ID, WorkerID: "someone-else",
				Status: dao.GenerationStatusSucceeded, Retention: testRetention,
			})
			require.ErrorIs(t, err, dao.ErrGenerationNotHeld)
		})
	})

	t.Run("Error/NonTerminalStatus", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)
			claimed := claimOne(ctx, t, testWorker)

			_, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
				ID: claimed[0].ID, WorkerID: testWorker,
				Status: dao.GenerationStatusRunning, Retention: testRetention,
			})
			require.ErrorIs(t, err, dao.ErrGenerationInvalidStatus)
		})
	})
}

// The provider call identifier is what stops a crash costing a second generation, so the two paths
// that touch it are the ones most worth pinning.
func TestGenerationProviderCallLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("Success/Record", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)
			claimed := claimOne(ctx, t, testWorker)

			daoRecord := dao.NewGenerationRecordProviderCall()

			recorded, err := daoRecord.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
				ID: claimed[0].ID, WorkerID: testWorker, ProviderCallID: "resp_1",
			})
			require.NoError(t, err)
			require.Equal(t, "resp_1", *recorded.ProviderCallID)

			// Recording again overwrites: the holder that started a replacement operation is the
			// authority on which identifier is live.
			recorded, err = daoRecord.Exec(ctx, &dao.GenerationRecordProviderCallRequest{
				ID: claimed[0].ID, WorkerID: testWorker, ProviderCallID: "resp_2",
			})
			require.NoError(t, err)
			require.Equal(t, "resp_2", *recorded.ProviderCallID)
		})
	})

	t.Run("Error/RecordWithoutHoldingTheClaim", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)
			claimed := claimOne(ctx, t, testWorker)

			_, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
				ID: claimed[0].ID, WorkerID: "someone-else", ProviderCallID: "resp_1",
			})
			require.ErrorIs(t, err, dao.ErrGenerationNotHeld)
		})
	})

	// The divergence that matters: a crash keeps the identifier so the next run re-attaches, a
	// declared failure clears it so the next run starts fresh.
	t.Run("Success/ReapPreservesTheIdentifierAndRequeueClearsIt", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			// Reaped: the worker died, the provider's operation did not.
			seed(ctx, t, 3)
			reapedClaim := claimOne(ctx, t, testWorker)

			_, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
				ID: reapedClaim[0].ID, WorkerID: testWorker, ProviderCallID: "resp_reaped",
			})
			require.NoError(t, err)

			expireLease(ctx, t, reapedClaim[0].ID, time.Hour)

			reaped, err := dao.NewGenerationReap().Exec(ctx, &dao.GenerationReapRequest{
				Grace: 0, Retention: testRetention,
			})
			require.NoError(t, err)
			require.Len(t, reaped, 1)
			require.Equal(t, dao.GenerationStatusPending, reaped[0].Status)
			require.NotNil(t, reaped[0].ProviderCallID)
			require.Equal(t, "resp_reaped", *reaped[0].ProviderCallID)

			// Requeued: the worker declared its operation dead.
			seed(ctx, t, 3)

			var requeueTarget *dao.Generation

			for _, generation := range claimOne(ctx, t, "worker-2") {
				if generation.ID != reapedClaim[0].ID {
					requeueTarget = generation
				}
			}

			require.NotNil(t, requeueTarget)

			_, err = dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
				ID: requeueTarget.ID, WorkerID: "worker-2", ProviderCallID: "resp_requeued",
			})
			require.NoError(t, err)

			requeued, err := dao.NewGenerationRequeue().Exec(ctx, &dao.GenerationRequeueRequest{
				ID: requeueTarget.ID, WorkerID: "worker-2",
			})
			require.NoError(t, err)
			require.Equal(t, dao.GenerationStatusPending, requeued.Status)
			require.Nil(t, requeued.ProviderCallID)
		})
	})
}

func TestGenerationReap(t *testing.T) {
	t.Parallel()

	t.Run("Success/AbandonsWithNoAttemptLeft", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)
			claimed := claimOne(ctx, t, testWorker)
			expireLease(ctx, t, claimed[0].ID, time.Hour)

			reaped, err := dao.NewGenerationReap().Exec(ctx, &dao.GenerationReapRequest{
				Grace: 0, Retention: testRetention,
			})
			require.NoError(t, err)
			require.Len(t, reaped, 1)
			require.Equal(t, dao.GenerationStatusAbandoned, reaped[0].Status)
			require.NotNil(t, reaped[0].SettledAt)
			require.NotNil(t, reaped[0].ExpiresAt)
		})
	})

	// A late settle has to beat the sweep, or a worker that finished just past its lease loses to
	// the reaper and the work is run twice.
	t.Run("Success/GraceLeavesARecentlyLapsedLeaseAlone", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)
			claimed := claimOne(ctx, t, testWorker)
			// Lapsed a minute ago, well inside an hour of grace.
			expireLease(ctx, t, claimed[0].ID, time.Minute)

			reaped, err := dao.NewGenerationReap().Exec(ctx, &dao.GenerationReapRequest{
				Grace: time.Hour, Retention: testRetention,
			})
			require.NoError(t, err)
			require.Empty(t, reaped)
		})
	})

	t.Run("Success/LeavesLiveLeasesAlone", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			seed(ctx, t, 1)
			claimOne(ctx, t, testWorker)

			reaped, err := dao.NewGenerationReap().Exec(ctx, &dao.GenerationReapRequest{
				Grace: 0, Retention: testRetention,
			})
			require.NoError(t, err)
			require.Empty(t, reaped)
		})
	})
}

func TestGenerationQueueDepth(t *testing.T) {
	t.Parallel()

	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()

		daoDepth := dao.NewGenerationQueueDepth()

		depth, err := daoDepth.Exec(ctx)
		require.NoError(t, err)
		require.Zero(t, depth.Pending)
		require.Zero(t, depth.OldestPendingAge())

		seed(ctx, t, 1)
		seed(ctx, t, 1)

		depth, err = daoDepth.Exec(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(2), depth.Pending)

		// A claimed generation is no longer backlog.
		claimOne(ctx, t, testWorker)

		depth, err = daoDepth.Exec(ctx)
		require.NoError(t, err)
		require.Zero(t, depth.Pending)
	})
}

func TestGenerationUsageInsert(t *testing.T) {
	t.Parallel()

	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()

		generation := seed(ctx, t, 1)
		claimed := claimOne(ctx, t, testWorker)

		daoUsage := dao.NewGenerationUsageInsert()

		request := &dao.GenerationUsageInsertRequest{
			GenerationID: claimed[0].ID,
			Attempt:      claimed[0].Attempt,
			OwnerID:      generation.OwnerID,
			Purpose:      generation.Purpose,
			Provider:     "openai",
			Model:        "a-model-snapshot",
			InputTokens:  1000, CachedInputTokens: 200,
			OutputTokens: 500, ReasoningTokens: 100,
		}

		usage, err := daoUsage.Exec(ctx, request)
		require.NoError(t, err)
		require.Equal(t, "a-model-snapshot", usage.Model)
		require.Equal(t, int64(1000), usage.InputTokens)

		// A settle that lands twice must not double-count.
		_, err = daoUsage.Exec(ctx, request)
		require.ErrorIs(t, err, dao.ErrGenerationUsageExists)
	})
}

// The usage row has no foreign key to its generation precisely so it can outlive the purge. This is
// the assertion that keeps that property from being "fixed" by adding one.
func TestGenerationUsageSurvivesItsGeneration(t *testing.T) {
	t.Parallel()

	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()

		generation := seed(ctx, t, 1)
		claimed := claimOne(ctx, t, testWorker)

		_, err := dao.NewGenerationUsageInsert().Exec(ctx, &dao.GenerationUsageInsertRequest{
			GenerationID: claimed[0].ID,
			Attempt:      claimed[0].Attempt,
			OwnerID:      generation.OwnerID,
			Purpose:      generation.Purpose,
			Provider:     "openai",
			Model:        "a-model-snapshot",
			InputTokens:  10, OutputTokens: 5,
		})
		require.NoError(t, err)

		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)

		// What the retention purge does.
		_, err = db.NewRaw("DELETE FROM generations WHERE id = ?0", generation.ID).Exec(ctx)
		require.NoError(t, err)

		var remaining int

		err = db.NewRaw(
			"SELECT count(*) FROM generation_usage WHERE generation_id = ?0", generation.ID,
		).Scan(ctx, &remaining)
		require.NoError(t, err)
		require.Equal(t, 1, remaining)
	})
}

// expireLease pushes a claim's lease the given age into the past, standing in for a worker that
// died that long ago.
func expireLease(ctx context.Context, t *testing.T, id uuid.UUID, age time.Duration) {
	t.Helper()

	db, err := postgres.GetContext(ctx)
	require.NoError(t, err)

	_, err = db.NewRaw(
		"UPDATE generations SET lease_expires_at = clock_timestamp() - make_interval(secs => ?1) WHERE id = ?0",
		id, age.Seconds(),
	).Exec(ctx)
	require.NoError(t, err)
}
