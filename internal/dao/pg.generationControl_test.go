package dao_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationControl(t *testing.T) {
	t.Parallel()
	postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		seedGeneration(ctx, t, 3)
		generation := claimGenerations(ctx, t)[0]
		_, err := dao.NewGenerationRequestCancel().Exec(ctx, &dao.GenerationRequestCancelRequest{
			ID:      generation.ID,
			OwnerID: generation.OwnerID,
		})
		require.NoError(t, err)
		current, err := dao.NewGenerationControl().Exec(ctx, &dao.GenerationControlRequest{
			ID: generation.ID, WorkerID: testWorker, ClaimToken: generation.ClaimToken, Lease: 2 * testLease,
		})
		require.NoError(t, err)
		require.NotNil(t, current.CancelRequestedAt)
		require.True(t, current.LeaseExpiresAt.After(*generation.LeaseExpiresAt))
		require.Equal(t, generation.Attempt, current.Attempt)
		require.Equal(t, generation.ClaimToken, current.ClaimToken)
	})
}

func TestGenerationClaimFences(t *testing.T) {
	t.Parallel()

	mutations := []struct {
		name string
		exec func(context.Context, *dao.Generation) error
	}{
		{"Control", func(ctx context.Context, g *dao.Generation) error {
			_, err := dao.NewGenerationControl().Exec(ctx, &dao.GenerationControlRequest{
				ID:         g.ID,
				WorkerID:   testWorker,
				ClaimToken: g.ClaimToken,
				Lease:      testLease,
			})

			return err
		}},
		{"BeginStart", func(ctx context.Context, g *dao.Generation) error {
			_, err := dao.NewGenerationBeginStart().Exec(ctx, &dao.GenerationBeginStartRequest{
				ID:         g.ID,
				WorkerID:   testWorker,
				ClaimToken: g.ClaimToken,
			})

			return err
		}},
		{"Record", func(ctx context.Context, g *dao.Generation) error {
			_, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
				ID:             g.ID,
				WorkerID:       testWorker,
				ClaimToken:     g.ClaimToken,
				ProviderCallID: "resp_fenced",
			})

			return err
		}},
		{"Settle", func(ctx context.Context, g *dao.Generation) error {
			_, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
				ID:         g.ID,
				WorkerID:   testWorker,
				ClaimToken: g.ClaimToken,
				Status:     dao.GenerationStatusSucceeded,
				Retention:  testRetention,
			})

			return err
		}},
		{"Fail", func(ctx context.Context, g *dao.Generation) error {
			_, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
				ID:         g.ID,
				WorkerID:   testWorker,
				ClaimToken: g.ClaimToken,
				Status:     dao.GenerationStatusFailed,
				Retention:  testRetention,
			})

			return err
		}},
		{"Cancel", func(ctx context.Context, g *dao.Generation) error {
			_, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
				ID:         g.ID,
				WorkerID:   testWorker,
				ClaimToken: g.ClaimToken,
				Status:     dao.GenerationStatusCancelled,
				Retention:  testRetention,
			})

			return err
		}},
		{"Requeue", func(ctx context.Context, g *dao.Generation) error {
			_, err := dao.NewGenerationRequeue().Exec(ctx, &dao.GenerationRequeueRequest{
				ID:             g.ID,
				WorkerID:       testWorker,
				ClaimToken:     g.ClaimToken,
				ProviderCallID: g.ProviderCallID,
			})

			return err
		}},
		{"ObserveLater", func(ctx context.Context, g *dao.Generation) error {
			_, err := dao.NewGenerationObserveLater().Exec(ctx, &dao.GenerationObserveLaterRequest{
				ID:         g.ID,
				WorkerID:   testWorker,
				ClaimToken: g.ClaimToken,
			})

			return err
		}},
	}
	for _, mutation := range mutations {
		for _, state := range []string{"Expired", "ReclaimedSameWorker", "ResumedSameAttempt"} {
			t.Run(mutation.name+"/"+state, func(t *testing.T) {
				t.Parallel()
				postgrestest.RunDBTest(
					t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
						t.Helper()
						seedGeneration(ctx, t, 3)

						stale := claimGenerations(ctx, t)[0]
						if state == "ResumedSameAttempt" || mutation.name == "ObserveLater" {
							recorded, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
								ID:             stale.ID,
								WorkerID:       testWorker,
								ClaimToken:     stale.ClaimToken,
								ProviderCallID: "resp_fenced",
							})
							require.NoError(t, err)

							stale = recorded
						}

						current := stale
						if state == "Expired" {
							expireLease(ctx, t, stale.ID, time.Hour)
						} else {
							var err error
							if state == "ResumedSameAttempt" {
								_, err = dao.NewGenerationObserveLater().Exec(ctx, &dao.GenerationObserveLaterRequest{
									ID:         stale.ID,
									WorkerID:   testWorker,
									ClaimToken: stale.ClaimToken,
								})
							} else {
								_, err = dao.NewGenerationRequeue().Exec(ctx, &dao.GenerationRequeueRequest{
									ID:             stale.ID,
									WorkerID:       testWorker,
									ClaimToken:     stale.ClaimToken,
									ProviderCallID: stale.ProviderCallID,
								})
							}

							require.NoError(t, err)
							current = claimGenerations(ctx, t)[0]
							require.NotEqual(t, stale.ClaimToken, current.ClaimToken)
							require.Equal(t, *stale.ClaimedBy, *current.ClaimedBy)

							if state == "ResumedSameAttempt" {
								require.Equal(t, stale.Attempt, current.Attempt)
							}
						}

						require.ErrorIs(t, mutation.exec(ctx, stale), dao.ErrGenerationNotHeld)
						after, err := dao.NewGenerationGet().Exec(ctx, &dao.GenerationGetRequest{
							ID:      current.ID,
							OwnerID: current.OwnerID,
						})
						require.NoError(t, err)
						require.Equal(t, current.ClaimToken, after.ClaimToken)
						require.Equal(t, current.ProviderCallID, after.ProviderCallID)
						require.Equal(t, dao.GenerationStatusRunning, after.Status)
					})
			})
		}
	}
}

func TestGenerationControlChecksExpiryAfterLock(t *testing.T) {
	t.Parallel()
	postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		seedGeneration(ctx, t, 3)
		generation := claimGenerations(ctx, t)[0]
		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)

		defer func() { _ = tx.Rollback() }()

		_, err = tx.NewRaw("SELECT id FROM generations WHERE id = ? FOR UPDATE", generation.ID).Exec(ctx)
		require.NoError(t, err)

		done := make(chan error, 1)

		go func() {
			_, controlErr := dao.NewGenerationControl().Exec(ctx, &dao.GenerationControlRequest{
				ID:         generation.ID,
				WorkerID:   testWorker,
				ClaimToken: generation.ClaimToken,
				Lease:      testLease,
			})
			done <- controlErr
		}()

		require.Eventually(t, func() bool {
			var waiting bool

			queryErr := db.NewRaw(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock')`).Scan(ctx, &waiting)

			return queryErr == nil && waiting
		}, 5*time.Second, time.Millisecond)

		_, err = tx.NewUpdate().Model((*dao.Generation)(nil)).
			Set("lease_expires_at = clock_timestamp() - interval '1 second'").
			Where("id = ?", generation.ID).Exec(ctx)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		require.ErrorIs(t, <-done, dao.ErrGenerationNotHeld)
	})
}

func TestGenerationClaimSkipsLockedWork(t *testing.T) {
	t.Parallel()
	postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		first := seedGeneration(ctx, t, 3)
		second := seedGeneration(ctx, t, 3)
		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)

		defer func() { _ = tx.Rollback() }()

		firstCtx := context.WithValue(ctx, postgres.ContextKey{}, bun.IDB(tx))
		one, err := dao.NewGenerationClaim().Exec(firstCtx, &dao.GenerationClaimRequest{
			WorkerID: "worker-a",
			Limit:    1,
			Lease:    testLease,
		})
		require.NoError(t, err)
		require.Len(t, one, 1)
		require.Equal(t, first.ID, one[0].ID)

		two, err := dao.NewGenerationClaim().Exec(ctx, &dao.GenerationClaimRequest{
			WorkerID: "worker-b",
			Limit:    1,
			Lease:    testLease,
		})
		require.NoError(t, err)
		require.Len(t, two, 1)
		require.Equal(t, second.ID, two[0].ID)
		require.NoError(t, tx.Commit())
	})
}

func TestGenerationBeginStart(t *testing.T) {
	t.Parallel()

	for _, cancelled := range []bool{false, true} {
		name := "Started"
		if cancelled {
			name = "Cancelled"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()
			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()
				seedGeneration(ctx, t, 3)

				generation := claimGenerations(ctx, t)[0]
				if cancelled {
					_, err := dao.NewGenerationRequestCancel().Exec(ctx, &dao.GenerationRequestCancelRequest{
						ID:      generation.ID,
						OwnerID: generation.OwnerID,
					})
					require.NoError(t, err)
				}

				started, err := dao.NewGenerationBeginStart().Exec(ctx, &dao.GenerationBeginStartRequest{
					ID:         generation.ID,
					WorkerID:   testWorker,
					ClaimToken: generation.ClaimToken,
				})
				require.NoError(t, err)

				if cancelled {
					require.Nil(t, started.StartRequestedAt)
				} else {
					require.NotNil(t, started.StartRequestedAt)
				}

				expireLease(ctx, t, generation.ID, time.Hour)
				reaped, err := dao.NewGenerationReap().Exec(ctx, &dao.GenerationReapRequest{
					Limit:     1,
					Retention: testRetention,
				})
				require.NoError(t, err)
				require.Len(t, reaped, 1)
				require.Equal(t, uuid.Nil, reaped[0].ClaimToken)

				if cancelled {
					require.Equal(t, dao.GenerationStatusCancelled, reaped[0].Status)
				} else {
					require.Equal(t, dao.GenerationStatusFailed, reaped[0].Status)
					require.Equal(t, "generation outcome unknown", *reaped[0].Error)
					require.NotNil(t, reaped[0].StartRequestedAt)
				}

				require.Empty(t, claimGenerations(ctx, t))
			})
		})
	}
}

func TestGenerationReaperSkipsRenewalAndSettlement(t *testing.T) {
	t.Parallel()

	for _, transition := range []string{"Renew", "Settle"} {
		t.Run(transition, func(t *testing.T) {
			t.Parallel()
			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()
				seedGeneration(ctx, t, 3)
				generation := claimGenerations(ctx, t)[0]
				db, err := postgres.GetContext(ctx)
				require.NoError(t, err)
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)

				defer func() { _ = tx.Rollback() }()

				txCtx := context.WithValue(ctx, postgres.ContextKey{}, bun.IDB(tx))
				if transition == "Renew" {
					_, err = dao.NewGenerationControl().Exec(txCtx, &dao.GenerationControlRequest{
						ID: generation.ID, WorkerID: testWorker, ClaimToken: generation.ClaimToken, Lease: testLease,
					})
				} else {
					_, err = dao.NewGenerationSettle().Exec(txCtx, &dao.GenerationSettleRequest{
						ID: generation.ID, WorkerID: testWorker, ClaimToken: generation.ClaimToken,
						Status: dao.GenerationStatusSucceeded, Retention: testRetention,
					})
				}

				require.NoError(t, err)
				// A broad sweep makes the old visible row eligible while its new version is still uncommitted.
				reaped, err := dao.NewGenerationReap().Exec(ctx, &dao.GenerationReapRequest{
					Grace: -2 * testLease, Limit: 10, Retention: testRetention,
				})
				require.NoError(t, err)
				require.Empty(t, reaped)
				require.NoError(t, tx.Commit())

				current, err := dao.NewGenerationGet().Exec(ctx, &dao.GenerationGetRequest{
					ID: generation.ID, OwnerID: generation.OwnerID,
				})
				require.NoError(t, err)

				if transition == "Renew" {
					require.Equal(t, generation.ClaimToken, current.ClaimToken)
				} else {
					require.Equal(t, dao.GenerationStatusSucceeded, current.Status)
				}
			})
		})
	}
}

func TestGenerationBeginStartObservesConcurrentCancellation(t *testing.T) {
	t.Parallel()
	postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		seedGeneration(ctx, t, 3)
		generation := claimGenerations(ctx, t)[0]
		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)

		defer func() { _ = tx.Rollback() }()

		txCtx := context.WithValue(ctx, postgres.ContextKey{}, bun.IDB(tx))
		_, err = dao.NewGenerationRequestCancel().Exec(txCtx, &dao.GenerationRequestCancelRequest{
			ID: generation.ID, OwnerID: generation.OwnerID,
		})
		require.NoError(t, err)

		done := make(chan *dao.Generation, 1)
		failures := make(chan error, 1)

		go func() {
			started, startErr := dao.NewGenerationBeginStart().Exec(ctx, &dao.GenerationBeginStartRequest{
				ID: generation.ID, WorkerID: testWorker, ClaimToken: generation.ClaimToken,
			})
			done <- started

			failures <- startErr
		}()

		require.Eventually(t, func() bool {
			var waiting bool

			queryErr := db.NewRaw(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock')`).Scan(ctx, &waiting)

			return queryErr == nil && waiting
		}, 5*time.Second, time.Millisecond)
		require.NoError(t, tx.Commit())
		require.NoError(t, <-failures)

		started := <-done
		require.NotNil(t, started.CancelRequestedAt)
		require.Nil(t, started.StartRequestedAt)
	})
}

func TestGenerationControlCannotReviveLeaseAfterReadLock(t *testing.T) {
	t.Parallel()
	postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()
		seedGeneration(ctx, t, 3)
		generation := claimGenerations(ctx, t)[0]
		db, err := postgres.GetContext(ctx)
		require.NoError(t, err)

		var deadline time.Time

		err = db.NewRaw(`UPDATE generations SET lease_expires_at = clock_timestamp() + interval '1 second'
WHERE id = ? RETURNING lease_expires_at`, generation.ID).Scan(ctx, &deadline)
		require.NoError(t, err)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)

		defer func() { _ = tx.Rollback() }()

		_, err = tx.NewRaw("SELECT id FROM generations WHERE id = ? FOR UPDATE", generation.ID).Exec(ctx)
		require.NoError(t, err)

		done := make(chan error, 1)

		go func() {
			_, controlErr := dao.NewGenerationControl().Exec(ctx, &dao.GenerationControlRequest{
				ID: generation.ID, WorkerID: testWorker, ClaimToken: generation.ClaimToken, Lease: testLease,
			})
			done <- controlErr
		}()

		require.Eventually(t, func() bool {
			var waiting bool

			queryErr := db.NewRaw(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock')`).Scan(ctx, &waiting)

			return queryErr == nil && waiting
		}, 5*time.Second, time.Millisecond)
		require.Eventually(t, func() bool {
			var expired bool

			queryErr := db.NewRaw("SELECT clock_timestamp() > ?", deadline).Scan(ctx, &expired)

			return queryErr == nil && expired
		}, 5*time.Second, time.Millisecond)
		require.NoError(t, tx.Commit())
		require.ErrorIs(t, <-done, dao.ErrGenerationNotHeld)
	})
}
