// Package config assembles the runtime configuration for the service: the typed
// structs the application reads, and the default preset that populates them from
// the environment.
package config

import (
	"time"

	"github.com/a-novel-kit/golib/logging"
	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

// Main holds the top-level application settings.
type Main struct {
	// Name of the application, as it appears in logs and tracing.
	Name string `json:"name" yaml:"name"`
	// DowntimeStart is when a planned downtime starts; nil when none is planned. From then until
	// it is removed, the service refuses work and leaves its database alone.
	DowntimeStart *time.Time `json:"downtimeStart" yaml:"downtimeStart"`
}

// Grpc holds the gRPC server configuration.
type Grpc struct {
	// Port on which the gRPC server listens for incoming requests.
	Port int `json:"port" yaml:"port"`
	// Ping is the refresh interval for the gRPC server's internal health check.
	Ping time.Duration `json:"ping" yaml:"ping"`
	// Shutdown bounds graceful RPC drain before remaining calls are stopped.
	Shutdown time.Duration `json:"shutdown" yaml:"shutdown"`
}

// Sweep holds the settings of the loop that checks generations nobody polled.
type Sweep struct {
	// Interval is how often the sweep runs, and how stale a generation must be for it.
	Interval time.Duration `json:"interval" yaml:"interval"`
	// BatchSize caps the generations one pass checks.
	BatchSize int `json:"batchSize" yaml:"batchSize"`
}

// TierBinding is what a Tier runs on the provider.
type TierBinding struct {
	Model           string `json:"model"           yaml:"model"`
	ReasoningEffort string `json:"reasoningEffort" yaml:"reasoningEffort"`
	MaxInputTokens  int64  `json:"maxInputTokens"  yaml:"maxInputTokens"`
	MaxOutputTokens int64  `json:"maxOutputTokens" yaml:"maxOutputTokens"`
}

// Provider holds the generative AI provider's settings. Switching provider changes these and nothing
// else.
type Provider struct {
	// Name identifies the provider on usage records.
	Name string `json:"name" yaml:"name"`
	// Epoch orders provider configurations; work started under a lower one restarts on this one.
	Epoch int32 `json:"epoch" yaml:"epoch"`
	// APIKey authenticates against the provider. The only credential this service holds.
	APIKey string `json:"-" yaml:"-"`
	// BaseURL is the Responses API endpoint.
	BaseURL string `json:"baseURL" yaml:"baseURL"`
	// Tiers binds each Tier, by name, to what it runs.
	Tiers map[string]TierBinding `json:"tiers" yaml:"tiers"`
}

// App is the complete configuration consumed by the service at startup, grouping
// the server, observability, logging, and database settings.
type App struct {
	App  Main `json:"app"  yaml:"app"`
	Grpc Grpc `json:"grpc" yaml:"grpc"`

	Sweep    Sweep    `json:"sweep"    yaml:"sweep"`
	Provider Provider `json:"provider" yaml:"provider"`
	// CheckInterval is how long a check stays fresh: a polled generation reaches the provider at most
	// once per interval.
	CheckInterval time.Duration `json:"checkInterval" yaml:"checkInterval"`
	// MaxAttempts caps the provider calls a generation gets when a call fails retryably.
	MaxAttempts int16 `json:"maxAttempts" yaml:"maxAttempts"`
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration `json:"retention" yaml:"retention"`

	Otel   otel.Config       `json:"otel"   yaml:"otel"`
	Logger logging.RPCConfig `json:"logger" yaml:"logger"`
	// Log is what checks and the sweep write to. The gRPC Logger above is an interceptor chain and
	// has no plain logging surface, which is what worker.Poll takes.
	Log      logging.Log     `json:"log"      yaml:"log"`
	Postgres postgres.Config `json:"postgres" yaml:"postgres"`
}
