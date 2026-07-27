package config

import (
	"github.com/uptrace/bun/driver/pgdriver"

	postgrespresets "github.com/a-novel-kit/golib/postgres/presets"

	"github.com/a-novel/service-genai/internal/config/env"
)

// PostgresPresetDefault is the default PostgreSQL connection configuration,
// built from the POSTGRES_DSN environment variable.
var PostgresPresetDefault = newPostgresPreset()

// newPostgresPreset builds the connection config with its pool bounded as it opens.
//
// Setting the limits on the handle afterwards stops working once anything has
// taken a connection, because the handle is cached; past that point they apply to
// nothing and report nothing.
func newPostgresPreset() *postgrespresets.Default {
	preset := postgrespresets.NewDefault(pgdriver.WithDSN(env.PostgresDsn))
	preset.MaxOpenConns = env.PostgresMaxOpenConns
	preset.MaxIdleConns = env.PostgresMaxIdleConns

	return preset
}
