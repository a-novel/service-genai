// Package env reads and parses the service's configuration from environment
// variables, exposing each setting as a typed, ready-to-use value.
package env

import (
	"os"
	"time"

	"github.com/a-novel-kit/golib/config"
)

// prefix is prepended to every configuration variable name that this package reads.
// Set SERVICE_GENAI_ENV_PREFIX when embedding the service in another project,
// where unprefixed variable names could collide with the host project's own.
var prefix = os.Getenv("SERVICE_GENAI_ENV_PREFIX")

func getEnv(name string) string {
	return os.Getenv(prefix + name)
}

// defaultWorkerID falls back to the hostname, which a container orchestrator already makes unique
// per replica. A worker without an identifier could not be told apart on a stranded claim.
func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil {
		return "worker"
	}

	return host
}

// Default values applied when an environment variable is unset.
const (
	AppNameDefault = "service-genai"

	GrpcPortDefault = 8080
	GrpcDefaultPing = time.Second * 5

	// WorkerIntervalDefault and the values below configure the generation worker. The lease is
	// sized to an expected run rather than a multiple of it: an outrun lease is recoverable, and
	// every lapse burns an attempt.
	WorkerIntervalDefault     = 5 * time.Second
	WorkerLeaseDefault        = 5 * time.Minute
	WorkerBatchSizeDefault    = 10
	WorkerPollIntervalDefault = 2 * time.Second

	// RetentionDefault is how long a settled generation's user content survives. Short on purpose:
	// it covers client retrieval, and the usage rows describing it are kept regardless.
	RetentionDefault = 7 * 24 * time.Hour

	// ReaperIntervalDefault and the values below configure the recovery sweep. The grace is the
	// head start a late settle gets over it.
	ReaperIntervalDefault  = 30 * time.Second
	ReaperGraceDefault     = 30 * time.Second
	ReaperBatchSizeDefault = 100

	// PostgresMaxOpenConnsDefault keeps the pool well under a stock PostgreSQL
	// max_connections of 100 once multiplied by a service's replica count, leaving
	// room for the migration job and a psql session. Go's own default is unlimited,
	// which turns a spike into connection refusals for everything on that database
	// rather than queueing inside this process.
	PostgresMaxOpenConnsDefault = 20
	// PostgresMaxIdleConnsDefault matches the open limit so a burst does not close
	// connections it is about to reopen.
	PostgresMaxIdleConnsDefault = 20
)

// Raw values for environment variables.
var (
	postgresDsn          = getEnv("POSTGRES_DSN")
	postgresMaxOpenConns = getEnv("POSTGRES_MAX_OPEN_CONNS")
	postgresMaxIdleConns = getEnv("POSTGRES_MAX_IDLE_CONNS")

	appName = getEnv("APP_NAME")
	otel    = getEnv("OTEL")

	grpcPort = getEnv("GRPC_PORT")
	grpcUrl  = getEnv("GRPC_URL")
	grpcPing = getEnv("GRPC_PING")

	openaiAPIKey  = getEnv("OPENAI_API_KEY")
	openaiBaseURL = getEnv("OPENAI_BASE_URL")

	workerID           = getEnv("WORKER_ID")
	workerInterval     = getEnv("WORKER_INTERVAL")
	workerLease        = getEnv("WORKER_LEASE")
	workerBatchSize    = getEnv("WORKER_BATCH_SIZE")
	workerPollInterval = getEnv("WORKER_POLL_INTERVAL")

	retention = getEnv("RETENTION")

	reaperInterval  = getEnv("REAPER_INTERVAL")
	reaperGrace     = getEnv("REAPER_GRACE")
	reaperBatchSize = getEnv("REAPER_BATCH_SIZE")

	gcloudProjectId = getEnv("GCLOUD_PROJECT_ID")
)

var (
	// PostgresDsn is the URL used to connect to the PostgreSQL database instance.
	// Typically formatted as:
	//	postgres://<user>:<password>@<host>:<port>/<database>
	PostgresDsn = postgresDsn

	// PostgresMaxOpenConns is the maximum number of open connections to the database.
	PostgresMaxOpenConns = config.LoadEnv(postgresMaxOpenConns, PostgresMaxOpenConnsDefault, config.IntParser)
	// PostgresMaxIdleConns is the maximum number of connections kept open while idle.
	PostgresMaxIdleConns = config.LoadEnv(postgresMaxIdleConns, PostgresMaxIdleConnsDefault, config.IntParser)

	// AppName is the name of the application, as it appears in logs and tracing.
	AppName = config.LoadEnv(appName, AppNameDefault, config.StringParser)
	// Otel enables OpenTelemetry instrumentation.
	//
	// See: https://opentelemetry.io/
	Otel = config.LoadEnv(otel, false, config.BoolParser)

	// GrpcPort is the port on which the gRPC server listens for incoming requests.
	GrpcPort = config.LoadEnv(grpcPort, GrpcPortDefault, config.IntParser)
	// GrpcUrl is the URL of the gRPC service, typically <host>:<port>.
	GrpcUrl = grpcUrl
	// GrpcPing is the refresh interval for the gRPC server's internal health check.
	GrpcPing = config.LoadEnv(grpcPing, GrpcDefaultPing, config.DurationParser)

	// OpenAIAPIKey authenticates against the provider. The only credential this service holds, and
	// the reason no consumer holds one.
	OpenAIAPIKey = openaiAPIKey
	// OpenAIBaseURL overrides the provider endpoint. Empty uses the SDK default; a value points at
	// an OpenAI-compatible provider or a local stand-in.
	OpenAIBaseURL = openaiBaseURL

	// WorkerID identifies this replica on the claims it holds. Empty takes the hostname, which is
	// what a container orchestrator already makes unique.
	WorkerID = config.LoadEnv(workerID, defaultWorkerID(), config.StringParser)
	// WorkerInterval is how often the worker looks for work when the queue is empty.
	WorkerInterval = config.LoadEnv(workerInterval, WorkerIntervalDefault, config.DurationParser)
	// WorkerLease is how long a claim holds before the reaper may recover it.
	WorkerLease = config.LoadEnv(workerLease, WorkerLeaseDefault, config.DurationParser)
	// WorkerBatchSize caps one claim.
	WorkerBatchSize = config.LoadEnv(workerBatchSize, WorkerBatchSizeDefault, config.IntParser)
	// WorkerPollInterval is how long the provider is given between polls of a running operation.
	WorkerPollInterval = config.LoadEnv(workerPollInterval, WorkerPollIntervalDefault, config.DurationParser)

	// Retention is how long a settled generation's user content survives before the purge.
	Retention = config.LoadEnv(retention, RetentionDefault, config.DurationParser)

	// ReaperInterval is how often the reaper sweeps for lapsed leases.
	ReaperInterval = config.LoadEnv(reaperInterval, ReaperIntervalDefault, config.DurationParser)
	// ReaperGrace is how long past its lease a claim is left alone, so a late settle beats the sweep.
	ReaperGrace = config.LoadEnv(reaperGrace, ReaperGraceDefault, config.DurationParser)
	// ReaperBatchSize caps one sweep.
	ReaperBatchSize = config.LoadEnv(reaperBatchSize, ReaperBatchSizeDefault, config.IntParser)

	// GcloudProjectId names the Google Cloud project the service runs in. Setting
	// it switches logging and tracing from the local console to Google Cloud.
	//
	// See: https://docs.cloud.google.com/resource-manager/docs/creating-managing-projects
	GcloudProjectId = gcloudProjectId
)
