package env

import (
	"net/http"
	"os"
	"time"

	"github.com/a-novel-kit/golib/config"
	"github.com/a-novel-kit/golib/downtime"
	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/httpf"
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

	// TimeoutShutdownDefault drains either server inside the ten seconds Cloud Run allows between
	// SIGTERM and SIGKILL, so an in-flight check finishes before the process is killed.
	TimeoutShutdownDefault = 8 * time.Second

	// CheckIntervalDefault is how long a check stays fresh: however often callers poll a generation,
	// it reaches the provider at most once per interval.
	CheckIntervalDefault = 2 * time.Second

	// OpenAIBaseURLDefault reaches OpenAI's own Responses API.
	OpenAIBaseURLDefault = "https://api.openai.com/v1/"

	// MaxAttemptsDefault lets one retryable provider failure be retried once. Each attempt is paid.
	MaxAttemptsDefault = 2

	// RetentionDefault is how long a settled generation survives: long enough for a caller to come back
	// after a lost connection, short enough that a request's content and its key are not kept.
	RetentionDefault = 6 * time.Hour

	// SweepIntervalDefault and SweepBatchSizeDefault configure the sweep that checks generations
	// nobody polled, well inside the time a finished provider result stays retrievable.
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

	grpcUrl = getEnv("GRPC_URL")

	openAIWebhookSecret = getEnv("OPENAI_WEBHOOK_SECRET")

	openAIAPIKey  = getEnv("OPENAI_API_KEY")
	openAIBaseURL = getEnv("OPENAI_BASE_URL")

	checkInterval = getEnv("CHECK_INTERVAL")
	maxAttempts   = getEnv("MAX_ATTEMPTS")

	retention = getEnv("RETENTION")

	sweepInterval  = getEnv("SWEEP_INTERVAL")
	sweepBatchSize = getEnv("SWEEP_BATCH_SIZE")

	gcloudProjectId = getEnv("GCLOUD_PROJECT_ID")

	downtimeStart = getEnv("DOWNTIME_START")
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

	// GrpcUrl is the URL of the gRPC service, typically <host>:<port>.
	GrpcUrl = grpcUrl
	// Rest configures the REST server from the REST_* variables.
	Rest = httpf.LoadServerConfig(getEnv, restDefaults())
	// Grpc configures the gRPC server from the GRPC_* variables.
	Grpc = grpcf.LoadServerConfig(getEnv, grpcDefaults())

	// OpenAIAPIKey authenticates the OpenAI connector. The only credential this service holds, and the
	// reason no caller holds one.
	OpenAIAPIKey = openAIAPIKey
	// OpenAIWebhookSecret is the signing secret of the webhook endpoint OpenAI notifies. Inject it as a
	// secret; unset, the service reads no notification.
	OpenAIWebhookSecret = openAIWebhookSecret
	// OpenAIBaseURL is the Responses API endpoint the OpenAI connector calls.
	OpenAIBaseURL = config.LoadEnv(openAIBaseURL, OpenAIBaseURLDefault, config.StringParser)

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

	// DowntimeStart is when a planned downtime of this service starts, in RFC 3339. From then
	// until it is removed, the service refuses work. Nil when none is planned.
	DowntimeStart = config.LoadEnv(downtimeStart, nil, downtime.ParseStart)
)

// restDefaults keeps golib's REST defaults but for three. The drain fits Cloud Run's kill window. The
// rate limit lets a provider's burst of notifications through, since they all come from its few
// addresses. The routes only read and receive notifications.
func restDefaults() httpf.ServerConfig {
	defaults := httpf.ServerConfigDefault
	defaults.Timeouts.Shutdown = TimeoutShutdownDefault
	defaults.RateLimit = config.Must(httpf.ParseRate("500/1s"))
	defaults.Cors.AllowedMethods = []string{http.MethodHead, http.MethodGet, http.MethodPost}

	return defaults
}

// grpcDefaults keeps golib's gRPC defaults but for the drain, which fits Cloud Run's kill window.
func grpcDefaults() grpcf.ServerConfig {
	defaults := grpcf.ServerConfigDefault
	defaults.Shutdown = TimeoutShutdownDefault

	return defaults
}
