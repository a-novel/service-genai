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

func TestGenerationElectCheck(t *testing.T) {
	t.Parallel()

	const interval = 30 * time.Second

	testCases := []struct {
		name string

		// checkedAgo is how long before the read the generation was last checked.
		checkedAgo time.Duration
		settled    bool
		owner      uuid.UUID
		// newerEpoch hands the generation to a newer provider configuration than the reader's.
		newerEpoch bool

		expectErr error
	}{
		{
			name: "Success",

			checkedAgo: time.Minute,
			owner:      testOwner,
		},
		{
			// Another poll checked it within the interval, so this one does not reach the provider.
			name: "Error/CheckedRecently",

			checkedAgo: time.Second,
			owner:      testOwner,

			expectErr: dao.ErrGenerationCheckNotDue,
		},
		{
			name: "Error/Settled",

			checkedAgo: time.Minute,
			settled:    true,
			owner:      testOwner,

			expectErr: dao.ErrGenerationCheckNotDue,
		},
		{
			// The reader would not check it, so it must not take the slot from a replica that would.
			name: "Error/NewerEpoch",

			checkedAgo: time.Minute,
			owner:      testOwner,
			newerEpoch: true,

			expectErr: dao.ErrGenerationCheckNotDue,
		},
		{
			name: "Error/OtherOwner",

			checkedAgo: time.Minute,
			owner:      uuid.MustParse("00000000-0000-0000-0000-000000000002"),

			expectErr: dao.ErrGenerationCheckNotDue,
		},
	}

	daoElectCheck := dao.NewGenerationElectCheck()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := runGeneration(ctx, t, seedGeneration(ctx, t, 1).ID)

				if testCase.settled {
					settleGeneration(ctx, t, generation)
				}

				if testCase.newerEpoch {
					execute(ctx, t, "UPDATE generations SET provider_epoch = ?1 WHERE id = ?0", generation.ID, testEpoch+1)
				}

				setCheckedAt(ctx, t, generation.ID, -testCase.checkedAgo)

				elected, err := daoElectCheck.Exec(ctx, &dao.GenerationElectCheckRequest{
					ID: generation.ID, OwnerID: testCase.owner, Interval: interval, ProviderEpoch: testEpoch,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, generation.ID, elected.ID)

				// The election itself refreshes the check, so a concurrent reader is refused.
				_, err = daoElectCheck.Exec(ctx, &dao.GenerationElectCheckRequest{
					ID: generation.ID, OwnerID: testCase.owner, Interval: interval, ProviderEpoch: testEpoch,
				})
				require.ErrorIs(t, err, dao.ErrGenerationCheckNotDue)
			})
		})
	}
}
