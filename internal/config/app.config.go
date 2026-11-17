package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/httpf"
	"github.com/a-novel-kit/golib/logging"
	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/servicetoken"
)

// ErrInvalidConfig reports a configuration the service cannot run with.
var ErrInvalidConfig = errors.New("invalid configuration")

// SweepIntervalCeiling bounds how long an unpolled generation goes unchecked. A finished background
// response stays retrievable for only a limited time when it is not stored, so the sweep must reach it
// well inside that window.
const SweepIntervalCeiling = 5 * time.Minute

// UnknownOutcomeHold is how long a generation that failed with its call's outcome unknown keeps its
// purpose's concurrency slot. The provider may still run a call that nothing tracks or meters, and
// the hold keeps a resend from multiplying that spend. The retention must outlast it, since the purge
// deletes the generation that holds the slot.
const UnknownOutcomeHold = 15 * time.Minute

// Main holds the top-level application settings.
type Main struct {
	// Name of the application, as it appears in logs and tracing.
	Name string
	// DowntimeStart is when a planned downtime starts; nil when none is planned. From then until
	// it is removed, the service refuses work and leaves its database alone.
	DowntimeStart *time.Time
}

// Sweep holds the settings of the sweep that checks generations nobody polled.
type Sweep struct {
	// Interval is how often the sweep runs. Each run checks every unsettled generation.
	Interval time.Duration
	// BatchSize caps the generations one batch of a sweep takes.
	BatchSize int
	// Concurrency caps the checks the sweep runs at once.
	Concurrency int
}

// Generations holds how generations are kept.
type Generations struct {
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration
}

// OpenAI holds how the OpenAI connector reaches the Responses API. The entrypoints convert it to the
// connector's own configuration, so its fields stay the same, in the same order.
type OpenAI struct {
	// BaseURL is the Responses API endpoint.
	BaseURL string
	// APIKey authenticates against OpenAI. It is the only credential this service holds.
	APIKey string `json:"-" yaml:"-"`
}

// App is the complete configuration consumed by the service at startup.
type App struct {
	// App holds the core application identity.
	App Main
	// Grpc holds the gRPC server configuration.
	Grpc grpcf.ServerConfig
	// Metrics holds the configuration of the server that reports the gRPC server's calls in flight.
	Metrics httpf.ServerConfig
	// Rest holds the configuration of the sweep's REST server.
	Rest httpf.ServerConfig
	// ServiceToken names the service tokens the gRPC server accepts. Without an issuer, the server
	// refuses to start unless ServiceIssuerOptional is set.
	ServiceToken servicetoken.Config
	// ServiceIssuerOptional lets the gRPC server start without a service-token issuer, and then
	// take every call. Only a local or test stack sets it.
	ServiceIssuerOptional bool
	// Generations holds how generations are run and kept.
	Generations Generations
	// Sweep configures the sweep that checks generations nobody polled.
	Sweep Sweep
	// OpenAI configures the OpenAI connector.
	OpenAI OpenAI
	// Purposes lists the purposes a caller may submit generations for. A submit or a usage summary
	// naming any other is refused.
	Purposes []string
	// Otel configures the OpenTelemetry exporter for traces and metrics.
	Otel otel.Config
	// Logger is the base application logger: checks log provider failures to it, and the REST handler
	// its errors.
	Logger logging.Log
	// GrpcLogger configures the gRPC request logging middleware.
	GrpcLogger logging.RPCConfig
	// RestLogger configures the REST request logging middleware.
	RestLogger logging.HTTPConfig
	// Postgres configures the PostgreSQL connection.
	Postgres postgres.Config
}

// Validate refuses a configuration the service could not run with, so a bad value fails startup.
func (app App) Validate() error {
	switch {
	case app.Generations.Retention < UnknownOutcomeHold:
		return fmt.Errorf("%w: the retention must be at least %s", ErrInvalidConfig, UnknownOutcomeHold)
	case app.Sweep.Interval <= 0 || app.Sweep.Interval > SweepIntervalCeiling:
		return fmt.Errorf("%w: the sweep interval must be positive and at most %s", ErrInvalidConfig, SweepIntervalCeiling)
	case app.Sweep.BatchSize < 1 || app.Sweep.BatchSize > 100:
		return fmt.Errorf("%w: the sweep batch size must be between 1 and 100", ErrInvalidConfig)
	case app.Sweep.Concurrency < 1:
		return fmt.Errorf("%w: the sweep concurrency must be positive", ErrInvalidConfig)
	}

	return nil
}
