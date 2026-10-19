// Package configtest holds configuration presets shared across the service's integration tests.
package configtest

import (
	"github.com/a-novel/service-genai/internal/config"
)

// PostgresPreset is the PostgreSQL configuration used in integration tests: the connection every
// preset shares, without the server's session timeouts.
var PostgresPreset = config.NewPostgresPreset()
