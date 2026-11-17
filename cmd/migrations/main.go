// Command migrations applies the service's database schema migrations, schedules its pg_cron jobs
// and exits. The cmd/grpc and cmd/sweep servers start once the schema is in place.
package main

import (
	"context"

	"github.com/samber/lo"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func main() {
	ctx := context.Background()

	db := lo.Must(config.PostgresPresetMigrations.DB(ctx))
	defer config.PostgresPresetMigrations.Close()

	lo.Must0(postgres.RunMigrations(ctx, db, migrations.Migrations))
	lo.Must(db.NewRaw(migrations.Jobs).Exec(ctx))
}
