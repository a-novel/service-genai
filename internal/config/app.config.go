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
)

// ErrInvalidConfig reports a configuration the service cannot run with.
var ErrInvalidConfig = errors.New("invalid configuration")

// SweepIntervalCeiling bounds how long an unpolled generation goes unchecked. A finished background
// response stays retrievable for only a limited time when it is not stored, so the sweep must reach it
// well inside that window.
const SweepIntervalCeiling = 5 * time.Minute

// MaxAttemptsCeiling bounds the provider calls a generation gets. A generation carries each answered
// call's response, at most 24 MiB, so two of them fit the 64 MiB message a client receives.
const MaxAttemptsCeiling = 2

// UnknownOutcomeHold is how long a generation that failed with its call's outcome unknown keeps its
// purpose's concurrency slot. The provider may still run a call that nothing tracks or meters, and
// the hold keeps a resend from multiplying that spend. The retention must outlast it, since the purge
// deletes the generation that holds the slot.
const UnknownOutcomeHold = 15 * time.Minute

// Main holds the top-level application settings.
type Main struct {
	// Name of the application, as it appears in logs and tracing.
	Name string `json:"name" yaml:"name"`
	// DowntimeStart is when a planned downtime starts; nil when none is planned. From then until
	// it is removed, the service refuses work and leaves its database alone.
	DowntimeStart *time.Time `json:"downtimeStart" yaml:"downtimeStart"`
}

// Sweep holds the settings of the sweep that checks generations nobody polled.
type Sweep struct {
	// Interval is how often the scheduler calls the sweep. Each call checks every generation nobody
	// checked within half of it.
	Interval time.Duration `json:"interval" yaml:"interval"`
	// BatchSize caps the generations one batch of a sweep takes.
	BatchSize int `json:"batchSize" yaml:"batchSize"`
	// Concurrency caps the checks the sweep runs at once, across every call that overlaps.
	Concurrency int `json:"concurrency" yaml:"concurrency"`
}

// Generations holds how generations are run and kept.
type Generations struct {
	// CheckInterval is how long a check stays fresh: a polled generation reaches the provider at most
	// once per interval.
	CheckInterval time.Duration `json:"checkInterval" yaml:"checkInterval"`
	// MaxAttempts caps the provider calls a generation gets when a call fails retryably.
	MaxAttempts int16 `json:"maxAttempts" yaml:"maxAttempts"`
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration `json:"retention" yaml:"retention"`
}

// OpenAI holds how the OpenAI connector reaches the Responses API. The entrypoints convert it to the
// connector's own configuration, so its fields stay the same, in the same order.
type OpenAI struct {
	// BaseURL is the Responses API endpoint.
	BaseURL string `json:"baseURL" yaml:"baseURL"`
	// APIKey authenticates against OpenAI. It is the only credential this service holds.
	APIKey string `json:"-" yaml:"-"`
	// WebhookSecret verifies OpenAI's webhook notifications. When it is empty, the service reads none.
	WebhookSecret string `json:"-" yaml:"-"`
}

// App is the complete configuration consumed by the service at startup.
type App struct {
	// App holds the core application identity.
	App Main `json:"app" yaml:"app"`
	// Grpc holds the gRPC server configuration.
	Grpc grpcf.ServerConfig `json:"grpc" yaml:"grpc"`
	// Rest holds the configuration of the REST servers: the notification server and the sweep.
	Rest httpf.ServerConfig `json:"rest" yaml:"rest"`
	// Generations holds how generations are run and kept.
	Generations Generations `json:"generations" yaml:"generations"`
	// Sweep configures the sweep that checks generations nobody polled.
	Sweep Sweep `json:"sweep" yaml:"sweep"`
	// OpenAI configures the OpenAI connector.
	OpenAI OpenAI `json:"openAi" yaml:"openAi"`
	// Caps is the usage policy every submit answers to.
	Caps Caps `json:"caps" yaml:"caps"`
	// Otel configures the OpenTelemetry exporter for traces and metrics.
	Otel otel.Config `json:"otel" yaml:"otel"`
	// Logger is the base application logger: checks log provider failures to it, and the REST handlers
	// their errors.
	Logger logging.Log `json:"logger" yaml:"logger"`
	// GrpcLogger configures the gRPC request logging middleware.
	GrpcLogger logging.RPCConfig `json:"grpclogger" yaml:"grpclogger"`
	// RestLogger configures the REST request logging middleware.
	RestLogger logging.HTTPConfig `json:"restLogger" yaml:"restLogger"`
	// Postgres configures the PostgreSQL connection.
	Postgres postgres.Config `json:"postgres" yaml:"postgres"`
}

// Validate refuses a configuration the service could not run with, so a bad value fails startup.
func (app App) Validate() error {
	switch {
	case app.Generations.CheckInterval <= 0:
		return fmt.Errorf("%w: the check interval must be positive", ErrInvalidConfig)
	case app.Generations.MaxAttempts < 1 || app.Generations.MaxAttempts > MaxAttemptsCeiling:
		return fmt.Errorf("%w: max attempts must be between 1 and %d", ErrInvalidConfig, MaxAttemptsCeiling)
	case app.Generations.Retention < UnknownOutcomeHold:
		return fmt.Errorf("%w: the retention must be at least %s", ErrInvalidConfig, UnknownOutcomeHold)
	case app.Sweep.Interval <= 0 || app.Sweep.Interval > SweepIntervalCeiling:
		return fmt.Errorf("%w: the sweep interval must be positive and at most %s", ErrInvalidConfig, SweepIntervalCeiling)
	case app.Sweep.BatchSize < 1 || app.Sweep.BatchSize > 100:
		return fmt.Errorf("%w: the sweep batch size must be between 1 and 100", ErrInvalidConfig)
	case app.Sweep.Concurrency < 1:
		return fmt.Errorf("%w: the sweep concurrency must be positive", ErrInvalidConfig)
	}

	err := app.Caps.Validate()
	if err != nil {
		return fmt.Errorf("caps: %w", err)
	}

	return nil
}
