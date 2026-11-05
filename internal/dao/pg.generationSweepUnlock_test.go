package dao_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationSweepUnlock(t *testing.T) {
	t.Parallel()

	daoLock := dao.NewPgGenerationSweepLock()
	daoUnlock := dao.NewPgGenerationSweepUnlock()

	testCases := []struct {
		name string

		// lock is the leader role the unlock releases: a held one, unless the case says otherwise.
		lock func(ctx context.Context, t *testing.T) *dao.GenerationSweepLock
	}{
		{
			// Released, the role passes to the next sweep.
			name: "Success",

			lock: func(ctx context.Context, t *testing.T) *dao.GenerationSweepLock {
				t.Helper()

				lock, err := daoLock.Exec(ctx, &dao.GenerationSweepLockRequest{})
				require.NoError(t, err)

				return lock
			},
		},
		{
			name: "Success/NothingHeld",

			lock: func(context.Context, *testing.T) *dao.GenerationSweepLock {
				return &dao.GenerationSweepLock{}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				err := daoUnlock.Exec(ctx, &dao.GenerationSweepUnlockRequest{Lock: testCase.lock(ctx, t)})
				require.NoError(t, err)

				next, err := daoLock.Exec(ctx, &dao.GenerationSweepLockRequest{})
				require.NoError(t, err)
				require.NoError(t, daoUnlock.Exec(ctx, &dao.GenerationSweepUnlockRequest{Lock: next}))
			})
		})
	}
}
