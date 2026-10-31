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

func TestJobs(t *testing.T) {
	t.Parallel()

	// jobsQuery reads the service's pg_cron jobs. Own reports whether the test's role owns the job.
	jobsQuery := `SELECT jobid, jobname, schedule, command, username = current_user AS own
		FROM cron.job WHERE jobname LIKE 'service-genai-%' ORDER BY jobname, jobid LIMIT 10`

	type cronJob struct {
		JobID int64 `bun:"jobid"`

		Name     string `bun:"jobname"`
		Schedule string `bun:"schedule"`
		Command  string `bun:"command"`
		Own      bool   `bun:"own"`
	}

	// expectJob is what a case checks of each job.
	type expectJob struct {
		Name     string
		Schedule string
		Own      bool
	}

	// An import leaves the jobs out, as the delete does.
	noJobs := "DELETE FROM cron.job WHERE jobname LIKE 'service-genai-%'"

	testCases := []struct {
		name string

		// setup prepares pg_cron's jobs before Jobs runs.
		setup []string

		expect []expectJob
	}{
		{
			name: "Success/Missing",

			setup: []string{noJobs},

			expect: []expectJob{
				{Name: "service-genai-purge-cron-runs", Schedule: "0 4 * * *", Own: true},
				{Name: "service-genai-purge-settled", Schedule: "10 seconds", Own: true},
			},
		},
		{
			// A database host hands the jobs to the service's role, and its migrations run as a
			// superuser. The job keeps its owner and takes the definition.
			name: "Success/OtherOwner",

			setup: []string{
				noJobs,
				"CREATE ROLE service_genai_jobs_owner LOGIN",
				`SELECT cron.schedule_in_database(
					'service-genai-purge-settled', '* * * * *', 'SELECT 1', current_database(),
					'service_genai_jobs_owner'
				)`,
			},

			expect: []expectJob{
				{Name: "service-genai-purge-cron-runs", Schedule: "0 4 * * *", Own: true},
				{Name: "service-genai-purge-settled", Schedule: "10 seconds", Own: false},
			},
		},
		{
			// A cluster runs its migrations as the database owner, which sees only its own jobs.
			name: "Success/DatabaseOwner",

			setup: []string{
				noJobs,
				"CREATE ROLE service_genai_jobs_app LOGIN",
				"GRANT USAGE ON SCHEMA cron TO service_genai_jobs_app",
				"SET LOCAL ROLE service_genai_jobs_app",
			},

			expect: []expectJob{
				{Name: "service-genai-purge-cron-runs", Schedule: "0 4 * * *", Own: true},
				{Name: "service-genai-purge-settled", Schedule: "10 seconds", Own: true},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// The test database has pg_cron, and the rollback leaves its jobs as they were.
			postgrestest.RunTransactionalTest(t, configtest.PostgresPreset, func(ctx context.Context, t *testing.T) {
				t.Helper()

				db, err := postgres.GetContext(ctx)
				require.NoError(t, err)

				for _, statement := range testCase.setup {
					_, err = db.NewRaw(statement).Exec(ctx)
					require.NoError(t, err)
				}

				_, err = db.NewRaw(migrations.Jobs).Exec(ctx)
				require.NoError(t, err)

				var jobs []cronJob

				err = db.NewRaw(jobsQuery).Scan(ctx, &jobs)
				require.NoError(t, err)
				require.Equal(t, testCase.expect, lo.Map(jobs, func(job cronJob, _ int) expectJob {
					return expectJob{Name: job.Name, Schedule: job.Schedule, Own: job.Own}
				}))

				purge, ok := lo.Find(jobs, func(job cronJob) bool { return job.Name == "service-genai-purge-settled" })
				require.True(t, ok)
				require.Equal(t, "SELECT public.purge_settled_generations()", purge.Command)

				// A second run changes nothing.
				_, err = db.NewRaw(migrations.Jobs).Exec(ctx)
				require.NoError(t, err)

				var rerun []cronJob

				err = db.NewRaw(jobsQuery).Scan(ctx, &rerun)
				require.NoError(t, err)
				require.Equal(t, jobs, rerun)
			})
		})
	}
}
