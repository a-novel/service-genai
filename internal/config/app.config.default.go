package config

import (
	"github.com/a-novel/service-genai/internal/config/env"
)

// AppPresetDefault is the configuration the service starts with. It reads every value from the
// environment. Once telemetry exports over OTLP, spans record each request and call, so the servers
// write no request log.
var AppPresetDefault = App{
	App: Main{
		Name:          env.AppName,
		DowntimeStart: env.DowntimeStart,
	},
	Grpc:         env.Grpc,
	Metrics:      env.Metrics,
	Rest:         env.Rest,
	ServiceToken: env.ServiceToken,

	Generations: Generations{
		Retention: env.Retention,
	},
	Sweep: Sweep{
		Interval:    env.SweepInterval,
		BatchSize:   env.SweepBatchSize,
		Concurrency: sweepConcurrency(env.Postgres.MaxOpenConns),
	},
	OpenAI: OpenAI{
		APIKey:  env.OpenAIAPIKey,
		BaseURL: env.OpenAIBaseURL,
	},
	Purposes: PurposesDefault,

	Otel:       env.Telemetry.Otel,
	Logger:     env.Telemetry.Logger,
	GrpcLogger: env.Telemetry.RPCLogger,
	RestLogger: env.Telemetry.HTTPLogger,
	Postgres:   PostgresPresetServer,
}

// sweepConcurrencyCeiling bounds the checks the sweep runs at once.
const sweepConcurrencyCeiling = 8

// sweepConcurrency gives each check a sweep runs at once a connection of its own, so none waits on
// the pool past its write deadlines. The sweep's leader role holds one more. A pool of zero is
// unlimited.
func sweepConcurrency(maxOpenConns int) int {
	if maxOpenConns <= 0 {
		return sweepConcurrencyCeiling
	}

	return max(min(maxOpenConns-1, sweepConcurrencyCeiling), 1)
}
