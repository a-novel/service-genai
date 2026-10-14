package dao_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationAttemptInsert(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// purgeParent deletes the generation the way the retention purge does.
		purgeParent bool

		response json.RawMessage

		expectErr error
	}{
		{
			// Stored as sent: the caller reads the provider's own bytes back.
			name: "Success",

			response: json.RawMessage(`{"status": "completed", "id": "resp_1", "usage": {"output_tokens": 5}}`),
		},
		{
			name: "Success/PurgedWithItsGeneration",

			purgeParent: true,
			response:    json.RawMessage(`{"status": "completed"}`),
		},
		{
			// An answer the column cannot hold fails however often it is sent.
			name: "Error/Refused",

			response: json.RawMessage(`{"status": `),

			expectErr: dao.ErrRefused,
		},
	}

	daoAttemptInsert := dao.NewPgGenerationAttemptInsert()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t, 1)
				running := runGeneration(ctx, t, generation.ID)

				attempt, err := daoAttemptInsert.Exec(ctx, &dao.GenerationAttemptInsertRequest{
					GenerationID: running.ID, Attempt: running.Attempt, Response: testCase.response,
				})
				require.ErrorIs(t, err, testCase.expectErr)

				if testCase.expectErr != nil {
					return
				}

				require.Equal(t, running.Attempt, attempt.Attempt)

				// Stored as sent: the caller reads the provider's own bytes back.
				stored, err := dao.NewPgGenerationAttemptList().Exec(ctx, &dao.GenerationAttemptListRequest{
					GenerationID: generation.ID,
				})
				require.NoError(t, err)
				require.Len(t, stored, 1)
				require.Equal(t, string(testCase.response), string(stored[0].Response))

				if !testCase.purgeParent {
					return
				}

				db, err := postgres.GetContext(ctx)
				require.NoError(t, err)

				_, err = db.NewRaw("DELETE FROM generations WHERE id = ?0", generation.ID).Exec(ctx)
				require.NoError(t, err)

				var remaining int

				err = db.NewRaw(
					"SELECT count(*) FROM generation_attempts WHERE generation_id = ?0", generation.ID,
				).Scan(ctx, &remaining)
				require.NoError(t, err)
				require.Zero(t, remaining)
			})
		})
	}
}
