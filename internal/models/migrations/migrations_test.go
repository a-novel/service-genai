package migrations_test

import (
	"context"
	"io/fs"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestMigrationRoundtrip(t *testing.T) {
	t.Parallel()

	_, err := fs.Stat(migrations.Migrations, "testdata")
	require.ErrorIs(t, err, fs.ErrNotExist)

	postgrestest.RunMigrationRoundtripTest(t, configtest.PostgresPreset, migrations.Migrations,
		&postgrestest.RoundtripOptions{
			Fixtures:  os.DirFS("testdata/fixtures"),
			Snapshots: "testdata/schema",
		})
}

func TestPurgeSettledGenerations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// expiresInSecs is when the generation's retention ends, from now; nil leaves it running.
		expiresInSecs *float64

		expectPurged bool
	}{
		{
			// Its payload and attempts go with it; its Usage Record stays.
			name: "Success/Expired",

			expiresInSecs: lo.ToPtr(-60.0),
			expectPurged:  true,
		},
		{
			name: "Success/WithinRetention",

			expiresInSecs: lo.ToPtr(60.0),
		},
		{
			name: "Success/Running",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				db, err := postgres.GetContext(ctx)
				require.NoError(t, err)

				id := uuid.Must(uuid.NewV7())

				// A settled generation carries its expiry, a running one its provider call. Each carries
				// what the purge must take with it, and a Usage Record it must leave.
				for _, seed := range []string{
					`INSERT INTO generations (
						id, owner_id, purpose, provider, request_key, status, attempt, start_requested_at,
						provider_call_id, settled_at, expires_at
					) SELECT
						?0, ?0, 'studio.generation', 'openai', ?0::text::bytea,
						(CASE WHEN secs IS NULL THEN 'running' ELSE 'succeeded' END)::generation_status, 1,
						CURRENT_TIMESTAMP, 'resp_1', CASE WHEN secs IS NOT NULL THEN CURRENT_TIMESTAMP END,
						CURRENT_TIMESTAMP + make_interval(secs => secs)
					FROM (SELECT ?1::float8 AS secs) AS expiry`,
					"INSERT INTO generation_payloads (generation_id, payload) VALUES (?0, '{}')",
					"INSERT INTO generation_attempts (generation_id, attempt, response) VALUES (?0, 1, '{}')",
					`INSERT INTO generation_usage (
						generation_id, attempt, owner_id, purpose, provider, model, class, input_tokens,
						output_tokens, started_at
					) VALUES (?0, 1, ?0, 'studio.generation', 'openai', 'a-model', 'none', 1, 1, CURRENT_TIMESTAMP)`,
				} {
					_, err = db.NewRaw(seed, id, testCase.expiresInSecs).Exec(ctx)
					require.NoError(t, err)
				}

				var purged int64

				err = db.NewRaw("SELECT purge_settled_generations()").Scan(ctx, &purged)
				require.NoError(t, err)
				require.Equal(t, int64(lo.Ternary(testCase.expectPurged, 1, 0)), purged)

				remaining := lo.Ternary(testCase.expectPurged, 0, 1)

				for query, expected := range map[string]int{
					"SELECT count(*) FROM generations WHERE id = ?0":                    remaining,
					"SELECT count(*) FROM generation_payloads WHERE generation_id = ?0": remaining,
					"SELECT count(*) FROM generation_attempts WHERE generation_id = ?0": remaining,
					"SELECT count(*) FROM generation_usage WHERE generation_id = ?0":    1,
				} {
					var count int

					err = db.NewRaw(query, id).Scan(ctx, &count)
					require.NoError(t, err)
					require.Equal(t, expected, count, query)
				}
			})
		})
	}
}
