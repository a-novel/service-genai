package env

import (
	"net/http"
	"os"
	"time"

	"github.com/a-novel-kit/golib/config"
	"github.com/a-novel-kit/golib/downtime"
	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/httpf"
	"github.com/a-novel-kit/golib/sentryf"
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

	// TimeoutShutdownDefault ends a server's drain 6 seconds after SIGTERM, so its Sentry and
	// OpenTelemetry flushes, up to 2 seconds each, finish before Cloud Run kills it at 10 seconds.
	TimeoutShutdownDefault = 6 * time.Second

	// TimeoutRequestDefault bounds a REST request, which runs its checks before it answers. A sweep call
	// checks for a minute, then waits out its last checks, which take a minute at most.
	TimeoutRequestDefault = 2 * time.Minute
	// TimeoutWriteDefault lets a request answer once it has run to its deadline.
	TimeoutWriteDefault = TimeoutRequestDefault + 15*time.Second

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

	// SweepIntervalDefault is how often the scheduler calls the sweep, well inside the time a finished
	// provider result stays retrievable.
	SweepIntervalDefault = time.Minute
	// SweepBatchSizeDefault is how many generations one batch of a sweep takes.
	SweepBatchSizeDefault = 50

	// PostgresMaxOpenConnsDefault caps the connections one instance opens. Multiplied by the replica
	// count, the cap stays well under PostgreSQL's default max_connections of 100. That leaves room
	// for the migration job and a psql session. A spike waits inside the process for a free connection.
	PostgresMaxOpenConnsDefault = 20
	// PostgresMaxIdleConnsDefault matches the open limit, so a burst keeps the connections it is about
	// to reuse.
	PostgresMaxIdleConnsDefault = 20
	// PostgresPortDefault is PostgreSQL's standard port.
	PostgresPortDefault = 5432
	// PostgresTLSEnabledDefault encrypts the connection unless a deployment turns it off.
	PostgresTLSEnabledDefault = true
)

var (
	// PostgresDSN is the legacy URL used when PostgresHost is empty, formatted as
	//	postgres://<user>:<password>@<host>:<port>/<database>
	//
	// Configure the discrete PostgreSQL fields for new deployments.
	PostgresDSN = getEnv("POSTGRES_DSN")
	// PostgresHost is the PostgreSQL server hostname or IP address.
	PostgresHost = getEnv("POSTGRES_HOST")
	// PostgresPort is the PostgreSQL server port.
	PostgresPort = config.LoadEnv(getEnv("POSTGRES_PORT"), PostgresPortDefault, config.IntParser)
	// PostgresUser is the PostgreSQL login role.
	PostgresUser = getEnv("POSTGRES_USER")
	// PostgresPassword is the PostgreSQL login credential.
	PostgresPassword = getEnv("POSTGRES_PASSWORD")
	// PostgresDatabase is the PostgreSQL database name.
	PostgresDatabase = getEnv("POSTGRES_DATABASE")
	// PostgresTLSEnabled controls transport encryption for the PostgreSQL connection.
	PostgresTLSEnabled = config.LoadEnv(getEnv("POSTGRES_TLS_ENABLED"), PostgresTLSEnabledDefault, config.BoolParser)

	// PostgresMaxOpenConns is the maximum number of open connections to the database.
	PostgresMaxOpenConns = config.LoadEnv(getEnv("POSTGRES_MAX_OPEN_CONNS"), PostgresMaxOpenConnsDefault, config.IntParser)
	// PostgresMaxIdleConns is the maximum number of connections kept open while idle.
	PostgresMaxIdleConns = config.LoadEnv(getEnv("POSTGRES_MAX_IDLE_CONNS"), PostgresMaxIdleConnsDefault, config.IntParser)

	// AppName is the name of the application, as it appears in logs and tracing.
	AppName = config.LoadEnv(getEnv("APP_NAME"), AppNameDefault, config.StringParser)
	// Otel enables OpenTelemetry tracing.
	Otel = config.LoadEnv(getEnv("OTEL"), false, config.BoolParser)

	// GrpcURL is the address of the gRPC service, in the form <host>:<port>. The Go client tests
	// dial it.
	GrpcURL = getEnv("GRPC_URL")
	// Rest configures the REST server from the REST_* variables.
	Rest = httpf.LoadServerConfig(getEnv, restDefaults())
	// Grpc configures the gRPC server from the GRPC_* variables.
	Grpc = grpcf.LoadServerConfig(getEnv, grpcDefaults())

	// OpenAIAPIKey authenticates the OpenAI connector. It is the only credential this service holds,
	// so no caller holds one.
	OpenAIAPIKey = getEnv("OPENAI_API_KEY")
	// OpenAIWebhookSecret is the signing secret of the webhook endpoint OpenAI notifies. Inject it as a
	// secret; unset, the service reads no notification.
	OpenAIWebhookSecret = getEnv("OPENAI_WEBHOOK_SECRET")
	// OpenAIBaseURL is the Responses API endpoint the OpenAI connector calls.
	OpenAIBaseURL = config.LoadEnv(getEnv("OPENAI_BASE_URL"), OpenAIBaseURLDefault, config.StringParser)

	// CheckInterval is how long a check stays fresh.
	CheckInterval = config.LoadEnv(getEnv("CHECK_INTERVAL"), CheckIntervalDefault, config.DurationParser)
	// MaxAttempts caps the provider calls a generation gets when a call fails retryably.
	MaxAttempts = config.LoadEnv(getEnv("MAX_ATTEMPTS"), MaxAttemptsDefault, config.Int16Parser)

	// Retention is how long a settled generation's user content survives before the purge.
	Retention = config.LoadEnv(getEnv("RETENTION"), RetentionDefault, config.DurationParser)

	// SweepInterval is how often the scheduler calls the sweep.
	SweepInterval = config.LoadEnv(getEnv("SWEEP_INTERVAL"), SweepIntervalDefault, config.DurationParser)
	// SweepBatchSize caps the generations one batch of a sweep takes.
	SweepBatchSize = config.LoadEnv(getEnv("SWEEP_BATCH_SIZE"), SweepBatchSizeDefault, config.IntParser)

	// GcloudProjectID is the Google Cloud project the service runs in. When set, logs and traces
	// go to Google Cloud. When empty, they go to standard output.
	GcloudProjectID = getEnv("GCLOUD_PROJECT_ID")

	// Sentry configures the report of server faults from the SENTRY_* variables. Without a DSN, the
	// servers report nothing.
	Sentry = sentryf.LoadConfig(getEnv)

	// DowntimeStart is when a planned downtime of this service starts, in RFC 3339. From then
	// until it is removed, the service refuses work. Nil when none is planned.
	DowntimeStart = config.LoadEnv(getEnv("DOWNTIME_START"), nil, downtime.ParseStart)
)

// restDefaults keeps golib's REST defaults but for a few. The drain fits Cloud Run's kill window, and a
// request has the time its checks need. The rate limit lets a provider's burst of notifications
// through, since they all come from its few addresses. The routes only read, receive notifications
// and sweep.
func restDefaults() httpf.ServerConfig {
	defaults := httpf.ServerConfigDefault
	defaults.Timeouts.Shutdown = TimeoutShutdownDefault
	defaults.Timeouts.Request = TimeoutRequestDefault
	defaults.Timeouts.Write = TimeoutWriteDefault
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
