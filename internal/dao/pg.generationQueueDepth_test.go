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

func TestPgGenerationQueueDepth(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		pending int
		// state is how far each seeded generation goes before the read.
		state string

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
			// A start in flight, or retrying after a refusal, still waits for the provider: counted, it
			// shows a provider that keeps refusing as a queue that stalls.
			name: "Success/StartInFlight",

			pending: 2,
			state:   "starting",

			expectPending: 2,
			expectWaiting: true,
		},
		{
			// A call the provider accepted is in flight, not backlog.
			name: "Success/RunningIsNotBacklog",

			pending: 2,
			state:   "running",
		},
	}

	daoQueueDepth := dao.NewPgGenerationQueueDepth()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				for range testCase.pending {
					generation := seedGeneration(ctx, t, 1)

					switch testCase.state {
					case "starting":
						startGeneration(ctx, t, generation.ID)
					case "running":
						runGeneration(ctx, t, generation.ID)
					}
				}

				depth, err := daoQueueDepth.Exec(ctx, &dao.GenerationQueueDepthRequest{})
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
