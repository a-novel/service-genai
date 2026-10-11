package config

import (
	"os"
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

// LoggerProdGrpc sends gRPC request logs to Google Cloud Logging, linked to their trace.
var LoggerProdGrpc = loggingpresets.GRPCGcloud{
	Component: env.AppName,
	ProjectId: env.GcloudProjectId,
}

// LoggerDevGrpc prints gRPC request logs to the console in a human-readable format.
var LoggerDevGrpc = loggingpresets.GRPCLocal{}

// LoggerDev prints application logs to the console in a human-readable format.
var LoggerDev = &loggingpresets.LogLocal{Out: os.Stdout}

// LoggerProd sends application logs to Google Cloud Logging.
var LoggerProd = &loggingpresets.LogGcloud{ProjectId: env.GcloudProjectId}

// AppPresetDefault is the configuration the service starts with. It reads every
// value from the environment, and picks the Google Cloud logging and tracing
// backends once a project ID is set.
var AppPresetDefault = App{
	App: Main{
		Name:          env.AppName,
		DowntimeStart: env.DowntimeStart,
	},
	Grpc: Grpc{
		Port:     env.GrpcPort,
		Ping:     env.GrpcPing,
		Shutdown: env.GrpcTimeoutShutdown,
	},

	Generations: Generations{
		CheckInterval: env.CheckInterval,
		MaxAttempts:   env.MaxAttempts,
		Retention:     env.Retention,
	},
	Sweep: Sweep{
		Interval:  env.SweepInterval,
		BatchSize: env.SweepBatchSize,
	},
	OpenAI: OpenAI{
		APIKey:  env.OpenAIAPIKey,
		BaseURL: env.OpenAIBaseURL,
	},
	Caps: CapsDefault,

	Otel: lo.If[otel.Config](!env.Otel, &otelpresets.Disabled{}).
		ElseIf(env.GcloudProjectId == "", &otelpresets.Local{
			FlushTimeout: OtelFlushTimeout,
		}).
		Else(&otelpresets.Gcloud{
			ProjectID:    env.GcloudProjectId,
			FlushTimeout: OtelFlushTimeout,
		}),
	Logger:     lo.Ternary[logging.Log](env.GcloudProjectId == "", LoggerDev, LoggerProd),
	GrpcLogger: lo.Ternary[logging.RPCConfig](env.GcloudProjectId == "", &LoggerDevGrpc, &LoggerProdGrpc),
	Postgres:   PostgresPresetDefault,
}
