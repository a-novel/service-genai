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

// PostgresPresetMigrations is the migrations job's connection. A migration waiting for a lock gives
// up after three seconds, so the queries queued behind it on its table proceed, and the runner tries
// it again later. Its statements are not bounded, since building an index can run long.
var PostgresPresetMigrations = NewPostgresPreset(pgdriver.WithConnParams(map[string]any{"lock_timeout": "3s"}))

// serverSessionTimeouts bound the server's sessions on the database side too: a statement, a lock
// wait or an abandoned transaction gives up there, rather than holding a connection the client has
// already left.
var serverSessionTimeouts = map[string]any{
	"statement_timeout":                   "9s",
	"lock_timeout":                        "5s",
	"idle_in_transaction_session_timeout": "30s",
}

// PostgresPresetServer is the servers' connection, with their session timeouts.
var PostgresPresetServer = NewPostgresPreset(pgdriver.WithConnParams(serverSessionTimeouts))

// NewPostgresPreset builds the connection config with its pool bounded as it opens. POSTGRES_HOST
// selects the discrete POSTGRES_* fields; without it, POSTGRES_DSN is used. Extra options apply
// last: connection parameters replace any a DSN carries.
//
// Setting the limits on the handle afterwards stops working once anything has
// taken a connection, because the handle is cached; past that point they apply to
// nothing and report nothing.
func NewPostgresPreset(extra ...pgdriver.Option) *postgrespresets.Default {
	connection := postgrespresets.Connection{
		DSN:         env.PostgresDsn,
		Host:        env.PostgresHost,
		Port:        env.PostgresPort,
		User:        env.PostgresUser,
		Password:    env.PostgresPassword,
		Database:    env.PostgresDatabase,
		TLSEnabled:  env.PostgresTLSEnabled,
		DialTimeout: postgresDialTimeout,
	}

	preset := postgrespresets.NewDefault(append(lo.Must(connection.Options()), extra...)...)
	preset.MaxOpenConns = env.PostgresMaxOpenConns
	preset.MaxIdleConns = env.PostgresMaxIdleConns

	return preset
}
