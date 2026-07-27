package dao_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationQueueDepth(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		pending int
		// claimed takes work out of the backlog before the read.
		claimed bool

		expectPending int
		expectWaiting bool
	}{
		{
			name: "Success/EmptyQueue",
		},
		{
			name: "Success/Pending",

			pending: 2,

			expectPending: 2,
			expectWaiting: true,
		},
		{
			// Claimed work is in flight, not backlog. Counting it would hide a stalled queue behind
			// a busy one.
			name: "Success/ClaimedWorkIsNotBacklog",

			pending: 2,
			claimed: true,
		},
	}

	daoQueueDepth := dao.NewGenerationQueueDepth()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				for range testCase.pending {
					seedGeneration(ctx, t, 1)
				}

				if testCase.claimed {
					claimGenerations(ctx, t)
				}

				depth, err := daoQueueDepth.Exec(ctx)
				require.NoError(t, err)
				require.Equal(t, int64(testCase.expectPending), depth.Pending)

				if testCase.expectWaiting {
					require.Positive(t, depth.OldestPendingAgeSeconds)
				} else {
					require.Zero(t, depth.OldestPendingAgeSeconds)
				}
			})
		})
	}
}
