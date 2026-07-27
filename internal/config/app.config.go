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
}

// Grpc holds the gRPC server configuration.
type Grpc struct {
	// Port on which the gRPC server listens for incoming requests.
	Port int `json:"port" yaml:"port"`
	// Ping is the refresh interval for the gRPC server's internal health check.
	Ping time.Duration `json:"ping" yaml:"ping"`
}

// Worker holds the generation worker's settings.
type Worker struct {
	// ID identifies this replica on the claims it holds.
	ID string `json:"id" yaml:"id"`
	// Interval is how often the worker looks for work when the queue is empty.
	Interval time.Duration `json:"interval" yaml:"interval"`
	// Lease is how long a claim holds before the reaper may recover it.
	Lease time.Duration `json:"lease" yaml:"lease"`
	// BatchSize caps one claim.
	BatchSize int `json:"batchSize" yaml:"batchSize"`
	// PollInterval is how long the provider is given between polls of a running operation.
	PollInterval time.Duration `json:"pollInterval" yaml:"pollInterval"`
}

// Reaper holds the settings of the loop that recovers generations a dead worker stranded.
type Reaper struct {
	// Interval is how often the reaper sweeps.
	Interval time.Duration `json:"interval" yaml:"interval"`
	// Grace is the head start a late settle gets over the sweep.
	Grace time.Duration `json:"grace" yaml:"grace"`
	// BatchSize caps one sweep.
	BatchSize int `json:"batchSize" yaml:"batchSize"`
}

// Provider holds the generative AI provider's settings.
type Provider struct {
	// APIKey authenticates against the provider. The only credential this service holds.
	APIKey string `json:"-" yaml:"-"`
	// BaseURL overrides the provider endpoint. Empty uses the client default.
	BaseURL string `json:"baseURL" yaml:"baseURL"`
}

// App is the complete configuration consumed by the service at startup, grouping
// the server, observability, logging, and database settings.
type App struct {
	App  Main `json:"app"  yaml:"app"`
	Grpc Grpc `json:"grpc" yaml:"grpc"`

	Worker   Worker   `json:"worker"   yaml:"worker"`
	Reaper   Reaper   `json:"reaper"   yaml:"reaper"`
	Provider Provider `json:"provider" yaml:"provider"`
	// Retention is how long a settled generation's user content survives before the purge.
	Retention time.Duration `json:"retention" yaml:"retention"`

	Otel   otel.Config       `json:"otel"   yaml:"otel"`
	Logger logging.RPCConfig `json:"logger" yaml:"logger"`
	// Log is what the background loops write to. The gRPC Logger above is an interceptor chain and
	// has no plain logging surface, which is what worker.Poll takes.
	Log      logging.Log     `json:"log"      yaml:"log"`
	Postgres postgres.Config `json:"postgres" yaml:"postgres"`
}
