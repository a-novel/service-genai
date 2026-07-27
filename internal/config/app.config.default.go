package config

import (
	"time"

	"github.com/samber/lo"

	"github.com/a-novel-kit/golib/logging"
	loggingpresets "github.com/a-novel-kit/golib/logging/presets"
	"github.com/a-novel-kit/golib/otel"
	otelpresets "github.com/a-novel-kit/golib/otel/presets"

	"github.com/a-novel/service-genai/internal/config/env"
)

const (
	// OtelFlushTimeout bounds how long the OpenTelemetry exporter waits to flush
	// pending spans on shutdown before giving up.
	OtelFlushTimeout = 2 * time.Second
)

// LoggerProd ships production logs to Google Cloud Logging.
var LoggerProd = loggingpresets.GRPCGcloud{
	Component: env.GcloudProjectId,
}

// LoggerDev pretty-prints logs to the console for local development.
var LoggerDev = loggingpresets.GRPCLocal{}

// AppPresetDefault is the configuration the service starts with. It reads every
// value from the environment, and picks the Google Cloud logging and tracing
// backends once a project ID is set.
var AppPresetDefault = App{
	App: Main{
		Name: env.AppName,
	},
	Grpc: Grpc{
		Port: env.GrpcPort,
		Ping: env.GrpcPing,
	},

	Otel: lo.If[otel.Config](!env.Otel, &otelpresets.Disabled{}).
		ElseIf(env.GcloudProjectId == "", &otelpresets.Local{
			FlushTimeout: OtelFlushTimeout,
		}).
		Else(&otelpresets.Gcloud{
			ProjectID:    env.GcloudProjectId,
			FlushTimeout: OtelFlushTimeout,
		}),
	Logger:   lo.Ternary[logging.RPCConfig](env.GcloudProjectId == "", &LoggerDev, &LoggerProd),
	Postgres: PostgresPresetDefault,
}
