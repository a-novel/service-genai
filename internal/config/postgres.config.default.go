package config

import (
	"time"

	"github.com/samber/lo"
	"github.com/uptrace/bun/driver/pgdriver"

	postgrespresets "github.com/a-novel-kit/golib/postgres/presets"

	"github.com/a-novel/service-genai/internal/config/env"
)

// postgresDialTimeout covers the network a Cloud Run instance with Direct VPC egress provisions
// after it starts. The first connection can wait minutes for it.
const postgresDialTimeout = 3 * time.Minute

// PostgresPresetDefault is the default PostgreSQL connection configuration. POSTGRES_HOST
// selects the discrete POSTGRES_* fields; without it, POSTGRES_DSN is used.
var PostgresPresetDefault = newPostgresPreset()

// PostgresPresetServer bounds the server's sessions on the database side too: a statement, a lock
// wait or an abandoned transaction gives up there, rather than holding a connection the client has
// already left. The migrations job keeps PostgresPresetDefault, since building an index can run long.
var PostgresPresetServer = newPostgresPreset(pgdriver.WithConnParams(map[string]any{
	"statement_timeout":                   "9s",
	"lock_timeout":                        "5s",
	"idle_in_transaction_session_timeout": "30s",
}))

// newPostgresPreset builds the connection config with its pool bounded as it opens. Extra options
// apply last: connection parameters replace any a DSN carries.
//
// Setting the limits on the handle afterwards stops working once anything has
// taken a connection, because the handle is cached; past that point they apply to
// nothing and report nothing.
func newPostgresPreset(extra ...pgdriver.Option) *postgrespresets.Default {
	preset := postgrespresets.NewDefault(append(lo.Must(postgrespresets.Connection{
		DSN:         env.PostgresDsn,
		Host:        env.PostgresHost,
		Port:        env.PostgresPort,
		User:        env.PostgresUser,
		Password:    env.PostgresPassword,
		Database:    env.PostgresDatabase,
		TLSEnabled:  env.PostgresTLSEnabled,
		DialTimeout: postgresDialTimeout,
	}.Options()), extra...)...)
	preset.MaxOpenConns = env.PostgresMaxOpenConns
	preset.MaxIdleConns = env.PostgresMaxIdleConns

	return preset
}
