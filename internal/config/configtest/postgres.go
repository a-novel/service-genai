// Package configtest holds configuration presets shared across the service's
// integration tests.
package configtest

import (
	"github.com/a-novel/service-genai/internal/config"
)

// PostgresPreset is the PostgreSQL configuration shared by integration tests,
// such as the DAO tests. It aliases the production preset, so tests connect the
// way the service does. Those tests run under postgres.RunDBTest: the migration
// set is applied once into a template database, and each test gets its own clone
// of it, dropped afterward — so tests never observe each other's writes. No
// transaction is opened, and postgres.InTx reports false, which is what a
// data-access object gating an outbound call needs.
//
// It lives in a regular (non-_test.go) file so other packages' tests can import
// it: Go excludes _test.go files from a package's exported surface. Keeping
// production code out of configtest is a convention enforced in review.
var PostgresPreset = config.PostgresPresetDefault
