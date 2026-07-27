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

// App is the complete configuration consumed by the service at startup, grouping
// the server, observability, logging, and database settings.
type App struct {
	App  Main `json:"app"  yaml:"app"`
	Grpc Grpc `json:"grpc" yaml:"grpc"`

	Otel     otel.Config       `json:"otel"     yaml:"otel"`
	Logger   logging.RPCConfig `json:"logger"   yaml:"logger"`
	Postgres postgres.Config   `json:"postgres" yaml:"postgres"`
}
