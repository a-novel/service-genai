package config

import (
	"os"
	"time"

	"github.com/samber/lo"

	"github.com/a-novel-kit/golib/logging"
	loggingpresets "github.com/a-novel-kit/golib/logging/presets"
	"github.com/a-novel-kit/golib/otel"
	otelpresets "github.com/a-novel-kit/golib/otel/presets"
	"github.com/a-novel-kit/golib/servicetoken"

	"github.com/a-novel/service-genai/internal/config/env"
)

// OtelFlushTimeout bounds how long the OpenTelemetry exporter waits to flush pending spans on
// shutdown.
const OtelFlushTimeout = 2 * time.Second

// SweepTelemetryTimeout bounds how long a sweep's answer waits for its spans and logs to leave. No
// consumer waits on the sweep, and the wait fits in the 15 seconds the write timeout leaves after the
// request's deadline.
const SweepTelemetryTimeout = 10 * time.Second

// OtelPresetDefault is the OpenTelemetry exporter of the servers. It exports nothing unless OTEL is
// set, then exports over OTLP/HTTP when OTEL_EXPORTER_OTLP_ENDPOINT is set, and to standard output
// otherwise. The liveness probe produces no span.
var OtelPresetDefault = newOtelPreset()

// exportsOTLP reports whether the servers export over OTLP: OTEL is set, and so is the OTLP
// endpoint. Their logs then go through the same backend, and their spans record each request.
var exportsOTLP = env.Otel && env.OTLPEndpoint != ""

// LoggerDevGrpc prints gRPC request logs to the console in a human-readable format.
var LoggerDevGrpc = loggingpresets.GRPCLocal{}

// LoggerDev prints application logs to the console in a human-readable format.
var LoggerDev = &loggingpresets.LogLocal{Out: os.Stdout}

// LoggerProd sends application logs through the OpenTelemetry backend, linked to their trace.
var LoggerProd = &loggingpresets.LogOTel{}

// AppPresetDefault is the configuration the service starts with. It reads every value from the
// environment, and logs through the OpenTelemetry backend when [exportsOTLP] holds. Spans then
// record each request and call, so the servers write no request log.
var AppPresetDefault = App{
	App: Main{
		Name:          env.AppName,
		DowntimeStart: env.DowntimeStart,
	},
	Grpc:    env.Grpc,
	Metrics: env.Metrics,
	Rest:    env.Rest,
	ServiceToken: servicetoken.Config{
		Issuer:   env.ServiceTokenIssuer,
		Audience: env.ServiceTokenAudience,
		Callers:  env.ServiceTokenCallers,
	},

	Generations: Generations{
		CheckInterval: env.CheckInterval,
		MaxAttempts:   env.MaxAttempts,
		Retention:     env.Retention,
	},
	Sweep: Sweep{
		Interval:    env.SweepInterval,
		BatchSize:   env.SweepBatchSize,
		Concurrency: sweepConcurrency(env.PostgresMaxOpenConns),
		Ticker:      env.SweepTicker,
	},
	OpenAI: OpenAI{
		APIKey:        env.OpenAIAPIKey,
		BaseURL:       env.OpenAIBaseURL,
		WebhookSecret: env.OpenAIWebhookSecret,
	},
	Caps: CapsDefault,

	Otel:       OtelPresetDefault,
	Logger:     lo.Ternary[logging.Log](!exportsOTLP, LoggerDev, LoggerProd),
	GrpcLogger: lo.Ternary[logging.RPCConfig](!exportsOTLP, &LoggerDevGrpc, &loggingpresets.GRPCOTel{}),
	RestLogger: lo.Ternary[logging.HTTPConfig](
		!exportsOTLP, &loggingpresets.HTTPLocal{BaseLogger: LoggerDev}, nil,
	),
	Postgres: PostgresPresetServer,
}

// newOtelPreset picks the exporter [OtelPresetDefault] describes.
func newOtelPreset() otel.Config {
	switch {
	case !env.Otel:
		return &otelpresets.Disabled{}
	case env.OTLPEndpoint == "":
		return &otelpresets.Local{FlushTimeout: OtelFlushTimeout}
	default:
		return &otelpresets.OTLP{UntracedPaths: []string{env.Rest.Liveness}, FlushTimeout: OtelFlushTimeout}
	}
}

// sweepConcurrencyCeiling bounds the checks the sweep runs at once.
const sweepConcurrencyCeiling = 8

// sweepConcurrency gives each check a sweep runs at once a connection of its own, so none waits on
// the pool past its write deadlines. A pool of zero is unlimited.
func sweepConcurrency(maxOpenConns int) int {
	if maxOpenConns <= 0 {
		return sweepConcurrencyCeiling
	}

	return min(maxOpenConns, sweepConcurrencyCeiling)
}
