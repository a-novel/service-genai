package env

import (
	"cmp"
	"os"
	"strconv"
	"time"

	"github.com/a-novel-kit/golib/config"
	"github.com/a-novel-kit/golib/downtime"
	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/httpf"
	otelpresets "github.com/a-novel-kit/golib/otel/presets"
	postgrespresets "github.com/a-novel-kit/golib/postgres/presets"
	"github.com/a-novel-kit/golib/servicetoken"
)

// prefix is prepended to every configuration variable name that this package reads.
// Set SERVICE_GENAI_ENV_PREFIX when embedding the service in another project,
// where unprefixed variable names could collide with the host project's own.
var prefix = os.Getenv("SERVICE_GENAI_ENV_PREFIX")

func getEnv(name string) string {
	return os.Getenv(prefix + name)
}

const (
	// AppNameDefault names the application in logs and traces when APP_NAME is unset.
	AppNameDefault = "service-genai"

	// MaxConcurrentCallsDefault bounds the gRPC calls one instance runs at once. Twenty submits at the
	// payload ceiling fit an instance of 2 GiB with GOMEMLIMIT at 1600MiB.
	MaxConcurrentCallsDefault = 20
	// MetricsPortDefault is the port that serves the gRPC server's calls in flight to an autoscaler.
	MetricsPortDefault = 9090

	// OpenAIBaseURLDefault reaches OpenAI's own Responses API.
	OpenAIBaseURLDefault = "https://api.openai.com/v1/"

	// RetentionDefault is how long a settled generation survives: long enough for a caller to come back
	// after a lost connection, short enough that a request's content and its key are not kept.
	RetentionDefault = 6 * time.Hour

	// SweepIntervalDefault is how often the sweep runs, well inside the time a finished provider result
	// stays retrievable.
	SweepIntervalDefault = time.Minute
	// SweepBatchSizeDefault is how many generations one batch of a sweep takes.
	SweepBatchSizeDefault = 50
)

var (
	// Postgres configures the database connection from the POSTGRES_* variables, with the fleet's
	// pool of 20 connections.
	Postgres = postgrespresets.LoadConnection(getEnv, postgrespresets.ConnectionDefault)

	// AppName is the name of the application, as it appears in logs and tracing.
	AppName = cmp.Or(getEnv("APP_NAME"), AppNameDefault)
	// Telemetry picks the OpenTelemetry exporter and the loggers from OTEL and the standard
	// OTEL_EXPORTER_OTLP_ENDPOINT: nothing exported, standard output, or OTLP/HTTP.
	Telemetry = otelpresets.LoadTelemetry(getEnv, otelpresets.ServerFlushTimeout)

	// GrpcURL is the address of the gRPC service, in the form <host>:<port>. The Go client tests
	// dial it.
	GrpcURL = getEnv("GRPC_URL")
	// Rest configures the sweep's REST server from the REST_* variables.
	Rest = httpf.LoadServerConfig(getEnv, httpf.ServerConfigDefault)
	// Grpc configures the gRPC server from the GRPC_* variables.
	Grpc = grpcf.LoadServerConfig(getEnv, grpcDefaults())
	// Metrics configures the server that reports the gRPC server's calls in flight, on METRICS_PORT.
	Metrics = metricsConfig()

	// ServiceToken names the service tokens the gRPC server accepts, from the SERVICE_TOKEN_* variables.
	// Without an issuer, the server refuses to start unless SERVICE_ISSUER_OPTIONAL is set, which only
	// a local or test stack does, as the standalone image does.
	ServiceToken = servicetoken.LoadConfig(getEnv)

	// OpenAIAPIKey authenticates the OpenAI connector. It is the only credential this service holds,
	// so no caller holds one.
	OpenAIAPIKey = getEnv("OPENAI_API_KEY")
	// OpenAIBaseURL is the Responses API endpoint the OpenAI connector calls.
	OpenAIBaseURL = cmp.Or(getEnv("OPENAI_BASE_URL"), OpenAIBaseURLDefault)

	// Retention is how long a settled generation's user content survives before the purge.
	Retention = config.LoadEnv(getEnv("RETENTION"), RetentionDefault, time.ParseDuration)

	// SweepInterval is how often the sweep runs.
	SweepInterval = config.LoadEnv(getEnv("SWEEP_INTERVAL"), SweepIntervalDefault, time.ParseDuration)
	// SweepBatchSize caps the generations one batch of a sweep takes.
	SweepBatchSize = config.LoadEnv(getEnv("SWEEP_BATCH_SIZE"), SweepBatchSizeDefault, strconv.Atoi)

	// DowntimeStart is when a planned downtime of this service starts, in RFC 3339. From then
	// until it is removed, the service refuses work. Nil when none is planned.
	DowntimeStart = config.LoadEnv(getEnv("DOWNTIME_START"), nil, downtime.ParseStart)
)

// grpcDefaults keeps golib's gRPC defaults but for the call cap, which bounds the memory requests
// hold.
func grpcDefaults() grpcf.ServerConfig {
	defaults := grpcf.ServerConfigDefault
	defaults.MaxConcurrentCalls = MaxConcurrentCallsDefault

	return defaults
}

// metricsConfig reads METRICS_PORT and keeps golib's REST defaults.
func metricsConfig() httpf.ServerConfig {
	metrics := httpf.ServerConfigDefault
	metrics.Port = config.LoadEnv(getEnv("METRICS_PORT"), MetricsPortDefault, strconv.Atoi)

	return metrics
}
