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

// Default values applied when an environment variable is unset.
const (
	AppNameDefault = "service-genai"

	GrpcPortDefault = 8080
	GrpcDefaultPing = time.Second * 5
	// GrpcTimeoutShutdownDefault fits inside the ten seconds Cloud Run allows between SIGTERM and
	// SIGKILL, so an in-flight check finishes before the process is killed.
	GrpcTimeoutShutdownDefault = 8 * time.Second

	// CheckIntervalDefault is how long a check stays fresh: however often callers poll a generation,
	// it reaches the provider at most once per interval.
	CheckIntervalDefault = 2 * time.Second

	// ProviderNameDefault and ProviderBaseURLDefault name and reach OpenAI, the committed provider.
	ProviderNameDefault    = "openai"
	ProviderBaseURLDefault = "https://api.openai.com/v1/"
	// ProviderEpochDefault is the first provider configuration. Each switch raises it.
	ProviderEpochDefault = 1

	// MaxAttemptsDefault lets one retryable provider failure be retried once. Each attempt is paid.
	MaxAttemptsDefault = 2

	// RetentionDefault is how long a settled generation survives: long enough for a caller to come back
	// after a lost connection, short enough that a request's content and its key are not kept.
	RetentionDefault = 6 * time.Hour

	// SweepIntervalDefault and SweepBatchSizeDefault configure the sweep that checks generations
	// nobody polled, well inside the ten minutes a finished provider result stays retrievable.
	SweepIntervalDefault  = time.Minute
	SweepBatchSizeDefault = 50

	// PostgresMaxOpenConnsDefault keeps the pool well under a stock PostgreSQL
	// max_connections of 100 once multiplied by a service's replica count, leaving
	// room for the migration job and a psql session. Go's own default is unlimited,
	// which turns a spike into connection refusals for everything on that database
	// rather than queueing inside this process.
	PostgresMaxOpenConnsDefault = 20
	// PostgresMaxIdleConnsDefault matches the open limit so a burst does not close
	// connections it is about to reopen.
	PostgresMaxIdleConnsDefault = 20
	PostgresPortDefault         = 5432
	PostgresTLSEnabledDefault   = true
)

// Raw values for environment variables.
var (
	postgresDsn          = getEnv("POSTGRES_DSN")
	postgresHost         = getEnv("POSTGRES_HOST")
	postgresPort         = getEnv("POSTGRES_PORT")
	postgresUser         = getEnv("POSTGRES_USER")
	postgresPassword     = getEnv("POSTGRES_PASSWORD")
	postgresDatabase     = getEnv("POSTGRES_DATABASE")
	postgresTLSEnabled   = getEnv("POSTGRES_TLS_ENABLED")
	postgresMaxOpenConns = getEnv("POSTGRES_MAX_OPEN_CONNS")
	postgresMaxIdleConns = getEnv("POSTGRES_MAX_IDLE_CONNS")

	appName = getEnv("APP_NAME")
	otel    = getEnv("OTEL")

	grpcPort            = getEnv("GRPC_PORT")
	grpcUrl             = getEnv("GRPC_URL")
	grpcPing            = getEnv("GRPC_PING")
	grpcTimeoutShutdown = getEnv("GRPC_TIMEOUT_SHUTDOWN")

	providerName    = getEnv("PROVIDER_NAME")
	providerEpoch   = getEnv("PROVIDER_EPOCH")
	providerBaseURL = getEnv("PROVIDER_BASE_URL")
	providerAPIKey  = getEnv("PROVIDER_API_KEY")
	providerTiers   = getEnv("PROVIDER_TIERS")

	checkInterval = getEnv("CHECK_INTERVAL")
	maxAttempts   = getEnv("MAX_ATTEMPTS")

	retention = getEnv("RETENTION")

	sweepInterval  = getEnv("SWEEP_INTERVAL")
	sweepBatchSize = getEnv("SWEEP_BATCH_SIZE")

	gcloudProjectId = getEnv("GCLOUD_PROJECT_ID")
)

var (
	// PostgresDsn is the connection URL, read only when PostgresHost is empty.
	// Typically formatted as:
	//	postgres://<user>:<password>@<host>:<port>/<database>
	PostgresDsn = postgresDsn
	// PostgresHost is the PostgreSQL server hostname or IP address. Setting it selects the
	// discrete connection fields below, so a deployment injects the password as a secret
	// without assembling a URL.
	PostgresHost = postgresHost
	// PostgresPort is the PostgreSQL server port.
	PostgresPort = config.LoadEnv(postgresPort, PostgresPortDefault, config.IntParser)
	// PostgresUser is the PostgreSQL login role.
	PostgresUser = postgresUser
	// PostgresPassword is the PostgreSQL login credential.
	PostgresPassword = postgresPassword
	// PostgresDatabase is the PostgreSQL database name.
	PostgresDatabase = postgresDatabase
	// PostgresTLSEnabled controls transport encryption for the PostgreSQL connection.
	PostgresTLSEnabled = config.LoadEnv(postgresTLSEnabled, PostgresTLSEnabledDefault, config.BoolParser)

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

	// GrpcTimeoutShutdown bounds graceful RPC drain before remaining calls are stopped.
	GrpcTimeoutShutdown = config.LoadEnv(
		grpcTimeoutShutdown, GrpcTimeoutShutdownDefault, config.DurationParser,
	)

	// ProviderName identifies the provider on usage records. Changed with the account or endpoint,
	// not on key rotation.
	ProviderName = config.LoadEnv(providerName, ProviderNameDefault, config.StringParser)
	// ProviderEpoch orders provider configurations: raised on every switch, so replicas running two
	// configurations during a rollout agree on which one wins.
	ProviderEpoch = config.LoadEnv(providerEpoch, ProviderEpochDefault, config.Int32Parser)
	// ProviderBaseURL is the Responses API endpoint.
	ProviderBaseURL = config.LoadEnv(providerBaseURL, ProviderBaseURLDefault, config.StringParser)
	// ProviderAPIKey authenticates against the provider. The only credential this service holds, and
	// the reason no consumer holds one.
	ProviderAPIKey = providerAPIKey
	// ProviderTiers is a JSON object binding each Tier to a model, an effort and token ceilings.
	// Empty keeps the committed OpenAI bindings.
	ProviderTiers = providerTiers

	// CheckInterval is how long a check stays fresh.
	CheckInterval = config.LoadEnv(checkInterval, CheckIntervalDefault, config.DurationParser)
	// MaxAttempts caps the provider calls a generation gets when a call fails retryably.
	MaxAttempts = config.LoadEnv(maxAttempts, MaxAttemptsDefault, config.Int16Parser)

	// Retention is how long a settled generation's user content survives before the purge.
	Retention = config.LoadEnv(retention, RetentionDefault, config.DurationParser)

	// SweepInterval is how often the sweep runs, and how stale a generation must be for it.
	SweepInterval = config.LoadEnv(sweepInterval, SweepIntervalDefault, config.DurationParser)
	// SweepBatchSize caps the generations one sweep pass checks.
	SweepBatchSize = config.LoadEnv(sweepBatchSize, SweepBatchSizeDefault, config.IntParser)

	// GcloudProjectId names the Google Cloud project the service runs in. Setting
	// it switches logging and tracing from the local console to Google Cloud.
	//
	// See: https://docs.cloud.google.com/resource-manager/docs/creating-managing-projects
	GcloudProjectId = gcloudProjectId
)
