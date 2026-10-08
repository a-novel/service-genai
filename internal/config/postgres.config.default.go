package config

import (
	"time"

	"github.com/samber/lo"

	postgrespresets "github.com/a-novel-kit/golib/postgres/presets"

	"github.com/a-novel/service-genai/internal/config/env"
)

// postgresDialTimeout covers the network a Cloud Run instance with Direct VPC egress provisions
// after it starts. The first connection can wait minutes for it.
const postgresDialTimeout = 3 * time.Minute

// PostgresPresetDefault is the default PostgreSQL connection configuration. POSTGRES_HOST
// selects the discrete POSTGRES_* fields; without it, POSTGRES_DSN is used.
var PostgresPresetDefault = newPostgresPreset()

// newPostgresPreset builds the connection config with its pool bounded as it opens.
//
// Setting the limits on the handle afterwards stops working once anything has
// taken a connection, because the handle is cached; past that point they apply to
// nothing and report nothing.
func newPostgresPreset() *postgrespresets.Default {
	preset := postgrespresets.NewDefault(lo.Must(postgrespresets.Connection{
		DSN:         env.PostgresDsn,
		Host:        env.PostgresHost,
		Port:        env.PostgresPort,
		User:        env.PostgresUser,
		Password:    env.PostgresPassword,
		Database:    env.PostgresDatabase,
		TLSEnabled:  env.PostgresTLSEnabled,
		DialTimeout: postgresDialTimeout,
	}.Options())...)
	preset.MaxOpenConns = env.PostgresMaxOpenConns
	preset.MaxIdleConns = env.PostgresMaxIdleConns

	return preset
}
