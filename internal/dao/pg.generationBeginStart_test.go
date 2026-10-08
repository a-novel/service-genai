package dao_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationBeginStart(t *testing.T) {
	t.Parallel()

	const epoch = testEpoch + 1

	testCases := []struct {
		name string

		// prepare moves the seeded generation into the state under test.
		prepare func(ctx context.Context, t *testing.T, id uuid.UUID)

		expectErr error
	}{
		{
			name: "Success",
		},
		{
			// A generation requeued on an older configuration starts again on the caller's.
			name: "Success/OlderEpoch",

			prepare: func(ctx context.Context, t *testing.T, id uuid.UUID) {
				t.Helper()
				execute(ctx, t, "UPDATE generations SET provider_epoch = ?1 WHERE id = ?0", id, epoch-1)
			},
		},
		{
			// A replica still on an older configuration must not take a generation back.
			name: "Error/NewerEpoch",

			prepare: func(ctx context.Context, t *testing.T, id uuid.UUID) {
				t.Helper()
				execute(ctx, t, "UPDATE generations SET provider_epoch = ?1 WHERE id = ?0", id, epoch+1)
			},

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/NotDue",

			prepare: func(ctx context.Context, t *testing.T, id uuid.UUID) {
				t.Helper()
				setRunAt(ctx, t, id, time.Hour)
			},

			expectErr: dao.ErrGenerationChanged,
		},
		{
			// A second check that read the same pending row must not send a second call.
			name: "Error/AlreadyStarting",

			prepare: func(ctx context.Context, t *testing.T, id uuid.UUID) {
				t.Helper()
				startGeneration(ctx, t, id)
			},

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/CancelRequested",

			prepare: func(ctx context.Context, t *testing.T, id uuid.UUID) {
				t.Helper()
				execute(ctx, t, "UPDATE generations SET cancel_requested_at = clock_timestamp() WHERE id = ?0", id)
			},

			expectErr: dao.ErrGenerationChanged,
		},
		{
			name: "Error/Running",

			prepare: func(ctx context.Context, t *testing.T, id uuid.UUID) {
				t.Helper()
				runGeneration(ctx, t, id)
			},

			expectErr: dao.ErrGenerationChanged,
		},
	}

	daoBeginStart := dao.NewGenerationBeginStart()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 1)

				if testCase.prepare != nil {
					testCase.prepare(ctx, t, generation.ID)
				}

				started, err := daoBeginStart.Exec(ctx, &dao.GenerationBeginStartRequest{
					ID: generation.ID, ProviderEpoch: epoch,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, dao.GenerationStatusPending, started.Status)
				require.Equal(t, int16(1), started.Attempt)
				require.NotNil(t, started.StartRequestedAt)
				require.Nil(t, started.ProviderCallID)
				require.Equal(t, new(epoch), started.ProviderEpoch)
			})
		})
	}
}
