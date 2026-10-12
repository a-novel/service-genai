// Command migrations applies the service's database schema migrations and exits.
// The cmd/grpc and cmd/rest entrypoints serve the generation API and the providers' notifications
// once the schema is in place.
package main

import (
	"context"

	"github.com/samber/lo"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func main() {
	ctx := lo.Must(postgres.NewContext(context.Background(), config.PostgresPresetDefault))
	lo.Must0(postgres.RunMigrationsContext(ctx, migrations.Migrations))
}
