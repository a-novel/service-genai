package dao_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationSweepLock(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// heldElsewhere has another sweep hold the leader role first.
		heldElsewhere bool
		// inTransaction runs the lock inside a transaction.
		inTransaction bool

		expectErr bool
		// expectLocked reports the refusal as the leader role being held.
		expectLocked bool
	}{
		{
			name: "Success",
		},
		{
			// One sweep runs at a time across every replica: the others skip their run.
			name: "Error/HeldElsewhere",

			heldElsewhere: true,

			expectErr:    true,
			expectLocked: true,
		},
		{
			// A transaction's connection serves its statements; a lock on it would outlive the sweep.
			name: "Error/InTransaction",

			inTransaction: true,

			expectErr: true,
		},
	}

	daoLock := dao.NewPgGenerationSweepLock()
	daoUnlock := dao.NewPgGenerationSweepUnlock()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// Each test database is its own, and so is its advisory lock space.
			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				if testCase.heldElsewhere {
					held, err := daoLock.Exec(ctx, &dao.GenerationSweepLockRequest{})
					require.NoError(t, err)

					defer func() {
						require.NoError(t, daoUnlock.Exec(ctx, &dao.GenerationSweepUnlockRequest{Lock: held}))
					}()
				}

				lockCtx := ctx

				if testCase.inTransaction {
					db, err := postgres.GetContext(ctx)
					require.NoError(t, err)

					tx, err := db.BeginTx(ctx, nil)
					require.NoError(t, err)

					defer func() { _ = tx.Rollback() }()

					lockCtx = postgres.WithTx(ctx, tx)
				}

				lock, err := daoLock.Exec(lockCtx, &dao.GenerationSweepLockRequest{})
				require.Equal(t, testCase.expectErr, err != nil, err)
				require.Equal(t, testCase.expectLocked, errors.Is(err, dao.ErrGenerationSweepLocked))

				if testCase.expectErr {
					require.Nil(t, lock)

					return
				}

				require.NotNil(t, lock)
				require.NoError(t, daoUnlock.Exec(ctx, &dao.GenerationSweepUnlockRequest{Lock: lock}))
			})
		})
	}
}
