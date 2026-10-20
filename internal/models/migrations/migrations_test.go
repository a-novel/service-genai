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

	roundtripOptions := &postgrestest.RoundtripOptions{
		Fixtures:  os.DirFS("testdata/fixtures"),
		Snapshots: "testdata/schema",
	}

	postgrestest.RunMigrationRoundtripTest(t, configtest.PostgresPreset, migrations.Migrations, roundtripOptions)
}

func TestPurgeSettledGenerations(t *testing.T) {
	t.Parallel()

	// A settled generation carries its expiry, a running one its provider call. Each carries what the
	// purge must take with it, and a Usage Record it must leave.
	seeds := []string{
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
	}

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

				for _, seed := range seeds {
					_, err = db.NewRaw(seed, id, testCase.expiresInSecs).Exec(ctx)
					require.NoError(t, err)
				}

				var purged int64

				err = db.NewRaw("SELECT purge_settled_generations()").Scan(ctx, &purged)
				require.NoError(t, err)
				require.Equal(t, int64(lo.Ternary(testCase.expectPurged, 1, 0)), purged)

				remaining := lo.Ternary(testCase.expectPurged, 0, 1)
				expectCounts := map[string]int{
					"SELECT count(*) FROM generations WHERE id = ?0":                    remaining,
					"SELECT count(*) FROM generation_payloads WHERE generation_id = ?0": remaining,
					"SELECT count(*) FROM generation_attempts WHERE generation_id = ?0": remaining,
					"SELECT count(*) FROM generation_usage WHERE generation_id = ?0":    1,
				}

				for query, expected := range expectCounts {
					var count int

					err = db.NewRaw(query, id).Scan(ctx, &count)
					require.NoError(t, err)
					require.Equal(t, expected, count, query)
				}
			})
		})
	}

	// expiredGenerations inserts ?0 generations whose retention ended a minute ago.
	expiredGenerations := `INSERT INTO generations (
		owner_id, purpose, provider, request_key, status, attempt, start_requested_at, provider_call_id,
		settled_at, expires_at
	) SELECT
		gen_random_uuid(), 'studio.generation', 'openai', uuid_send(gen_random_uuid()), 'succeeded', 1,
		CURRENT_TIMESTAMP, 'resp_1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP - interval '1 minute'
	FROM generate_series(1, ?0)`

	batchCases := []struct {
		name string

		generations int
		// content stores what the generations hold.
		content []string
		purge   string

		// expectPurged is what each successive call purges.
		expectPurged []int64
	}{
		{
			// A backlog drains one bounded batch a call, so no call holds its locks long.
			name: "Success/Backlog",

			generations: 1001,
			purge:       "SELECT purge_settled_generations()",

			expectPurged: []int64{1000, 1, 0},
		},
		{
			// A call stops at its byte budget, but always takes a generation, so large ones still drain.
			name: "Success/ByteBudget",

			generations: 2,
			content: []string{
				"INSERT INTO generation_payloads (generation_id, payload) SELECT id, '{}' FROM generations",
				"INSERT INTO generation_attempts (generation_id, attempt, response) SELECT id, 1, '{}' FROM generations",
			},
			purge: "SELECT purge_settled_generations(1)",

			expectPurged: []int64{1, 1, 0},
		},
	}

	for _, testCase := range batchCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				db, err := postgres.GetContext(ctx)
				require.NoError(t, err)

				_, err = db.NewRaw(expiredGenerations, testCase.generations).Exec(ctx)
				require.NoError(t, err)

				for _, statement := range testCase.content {
					_, err = db.NewRaw(statement).Exec(ctx)
					require.NoError(t, err)
				}

				for _, expected := range testCase.expectPurged {
					var purged int64

					err = db.NewRaw(testCase.purge).Scan(ctx, &purged)
					require.NoError(t, err)
					require.Equal(t, expected, purged)
				}
			})
		})
	}
}
