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

// LoggerProd ships production logs to Google Cloud Logging.
var LoggerProd = loggingpresets.GRPCGcloud{
	Component: env.GcloudProjectId,
}

// LoggerDev pretty-prints logs to the console for local development.
var LoggerDev = loggingpresets.GRPCLocal{}

// LogDev and LogProd are what the background loops write to.
var (
	LogDev = &loggingpresets.LogLocal{Out: os.Stdout}

	LogProd = &loggingpresets.LogGcloud{ProjectId: env.GcloudProjectId}
)

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

	Worker: Worker{
		ID:           env.WorkerID,
		Interval:     env.WorkerInterval,
		Lease:        env.WorkerLease,
		BatchSize:    env.WorkerBatchSize,
		PollInterval: env.WorkerPollInterval,
	},
	Reaper: Reaper{
		Interval:  env.ReaperInterval,
		Grace:     env.ReaperGrace,
		BatchSize: env.ReaperBatchSize,
	},
	Provider: Provider{
		APIKey:  env.OpenAIAPIKey,
		BaseURL: env.OpenAIBaseURL,
	},
	Retention: env.Retention,

	Otel: lo.If[otel.Config](!env.Otel, &otelpresets.Disabled{}).
		ElseIf(env.GcloudProjectId == "", &otelpresets.Local{
			FlushTimeout: OtelFlushTimeout,
		}).
		Else(&otelpresets.Gcloud{
			ProjectID:    env.GcloudProjectId,
			FlushTimeout: OtelFlushTimeout,
		}),
	Logger:   lo.Ternary[logging.RPCConfig](env.GcloudProjectId == "", &LoggerDev, &LoggerProd),
	Log:      lo.Ternary[logging.Log](env.GcloudProjectId == "", LogDev, LogProd),
	Postgres: PostgresPresetDefault,
}
