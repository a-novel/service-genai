// Package configtest holds shared test fixtures for the config package. Only `_test.go` files
// may import it.
//
// Isolation is a convention the language does not enforce: Go links any package reachable from
// a production import into the binary, so keeping configtest out of every production import
// path is what confines these fixtures to the test binary. A dedicated subpackage makes that
// boundary easy to lint or grep against.
package configtest

import (
	"github.com/a-novel/service-genai/internal/config"
)

// PostgresPreset is the PostgreSQL configuration used in integration tests: the connection every
// preset shares, without the server's session timeouts.
var PostgresPreset = config.NewPostgresPreset()
