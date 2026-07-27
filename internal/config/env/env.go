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

	// GcloudProjectId names the Google Cloud project the service runs in. Setting
	// it switches logging and tracing from the local console to Google Cloud.
	//
	// See: https://docs.cloud.google.com/resource-manager/docs/creating-managing-projects
	GcloudProjectId = gcloudProjectId
)
