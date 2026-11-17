package config

import (
	"github.com/samber/lo"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/a-novel/service-genai/internal/config/env"
)

// PostgresPresetMigrations is the migrations job's connection. A migration waiting for a lock gives
// up after three seconds, so the queries queued behind it on its table proceed, and the runner tries
// it again later. Its statements are not bounded, since building an index can run long.
var PostgresPresetMigrations = lo.Must(env.Postgres.Preset(
	pgdriver.WithConnParams(map[string]any{"lock_timeout": "3s"}),
))

// serverSessionTimeouts bound the server's sessions on the database side too: a statement, a lock
// wait or an abandoned transaction gives up there, rather than holding a connection the client has
// already left.
var serverSessionTimeouts = map[string]any{
	"statement_timeout":                   "9s",
	"lock_timeout":                        "5s",
	"idle_in_transaction_session_timeout": "30s",
}

// PostgresPresetServer is the servers' connection, with their session timeouts.
var PostgresPresetServer = lo.Must(env.Postgres.Preset(pgdriver.WithConnParams(serverSessionTimeouts)))
