package migrations

import (
	"embed"
)

// Migrations is the embedded filesystem of SQL migration files. Pass it to the migration runner to
// bring a database up to the service's current schema. Each timestamped file has an .up.sql step to
// apply the change and a matching .down.sql step to revert it.
//
//go:generate env GOLIB_UPDATE_SNAPSHOTS=1 go test -run ^TestMigrationRoundtrip$
//go:embed *.sql
var Migrations embed.FS

// Jobs is the SQL that schedules the service's pg_cron jobs, such as the retention purge. Run it
// after [Migrations] on every run: a database import keeps the migration history but not the jobs.
// A second run changes nothing, and a database without pg_cron skips it.
//
//go:embed cron/jobs.sql
var Jobs string
