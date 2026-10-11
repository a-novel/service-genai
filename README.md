# GenAI service

Every AI provider call on the platform runs here: the caller builds the request, and this service executes it durably, never pays for it twice, and returns what the provider answered.

[![X (formerly Twitter) Follow](https://img.shields.io/twitter/follow/agorastoryverse)](https://twitter.com/agorastoryverse)
[![Discord](https://img.shields.io/discord/1315240114691248138?logo=discord)](https://discord.gg/rp4Qr8cA)

<hr />

![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/a-novel/service-genai)
![GitHub repo file or directory count](https://img.shields.io/github/directory-file-count/a-novel/service-genai)
![GitHub code size in bytes](https://img.shields.io/github/languages/code-size/a-novel/service-genai)

![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/a-novel/service-genai/main.yaml)
[![codecov](https://codecov.io/gh/a-novel/service-genai/graph/badge.svg)](https://codecov.io/gh/a-novel/service-genai)

![Coverage graph](https://codecov.io/gh/a-novel/service-genai/graphs/sunburst.svg)

## What it does

A provider call can take minutes, costs real money, and must never be paid for twice. This service owns how such a call executes; the caller owns what is executed. It never reads the request it runs or the answers it returns.

It is an execution service, not a proxy. The caller holds no connection while the provider works: it submits the call, then polls for the result, so the call outlives the caller's request, a restart of either side, and a deploy.

Five things shape the contract:

**The caller builds the request.** A submission names why it is made (its `purpose`, in the caller's own vocabulary) and a provider (`openai` today), and carries the payload: the provider's own request body, as raw JSON. The model, the prompt, the output schema, the limits and the privacy fields such as `store` belong to the caller. The provider's connector writes only the fields the execution mode needs, and authenticates with the platform's only provider credential. Calls run in the Async mode: the provider accepts the call and runs it on its side while the caller polls.

**A resend is a replay.** The service derives each generation's key from its owner, its purpose, its provider and its payload, so a caller that lost its connection or crashed sends the same request again and gets the same generation back, running or finished. It keeps no key of its own; the same bytes are the same call. A failed or cancelled generation is not served again: resending its request runs it from scratch. To ask for another result of a request that succeeded, send it with the next `variant`.

**A crash re-attaches instead of re-paying.** The provider's own identifier for an in-flight operation is recorded the moment the call starts, and every later check reads it back. No process holds a generation, so a restart or a deploy loses nothing: the next poll or sweep picks up the operation already paid for. Every accepted call is followed until it settles, whether anyone polls it or not.

**The provider's answers come back as sent.** Each generation lists every attempt that reached an answer, retried and cancelled ones included, with the provider's raw response, which holds the output and the usage. The service also keeps a permanent Usage Record of every answered attempt, with no content: who and what it was for, its token counts, the model, reasoning effort and service tier that ran, and when it started and finished. The generation and its responses are purged after `RETENTION`; the usage stays. A generation that holds no finished response says why in the service's own words, and a request the provider rejected comes back with the provider's error body.

**Usage is capped per purpose and user tier.** The [caps file](./internal/config/caps.config.yaml), built into the image so every change is reviewed, declares each purpose a caller may submit, the token limits each user tier gets over rolling windows, and how many generations a user may run at once, per purpose and across all of them; its comments explain every key. Once a user has reached a limit, a submit that would start a new generation answers `RESOURCE_EXHAUSTED`, naming it. An admitted call always finishes, even past its cap, and a resend of a running generation always attaches to it.

The surface is **gRPC only**. Callers are other services on the internal network, so there is no browser client and no REST API to keep in step.

## Deploying

The service runs as published OCI images plus a PostgreSQL database. All state lives in Postgres, so the server scales to as many replicas as you need; replicas coordinate through conditional writes on each generation row.

> **OpenTofu modules are the planned canonical deployment path.** Until they land, deploy the images with any container orchestrator — the composition below is the reference for which images to run, how they wire together, and the environment they expect.

| Image                           | Role                                                                        |
| ------------------------------- | --------------------------------------------------------------------------- |
| `service-genai/grpc`            | The generation API and its sweep. Internal network only.                    |
| `service-genai/jobs/migrations` | One-shot schema migration job; runs to completion before the server starts. |
| `service-genai/database`        | PostgreSQL with `pg_cron` and pgBackRest — or bring your own Postgres.      |
| `service-genai/standalone-grpc` | Server plus migrations in one image. Local development only.                |

Pin every image to the same release tag — see the [latest release](https://github.com/a-novel/service-genai/releases/latest). A production deployment runs `database`, then the migrations job to completion, then any number of `grpc` replicas. Provide `POSTGRES_HOST`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DATABASE` and `OPENAI_API_KEY` through the orchestrator; store the password and the API key as secrets. A caller controls the whole payload, including the model and the provider's built-in tools, so grant invocation to calling services only and restrict the OpenAI key to the Responses endpoints.

Callers poll for results, and each poll checks its generation with the provider. The sweep inside the `grpc` process checks the generations nobody polled, so a result is captured before the provider discards it. Give each replica CPU outside of requests (on Cloud Run, CPU always allocated with at least one instance) and outbound access to the provider's API.

```yaml
services:
  postgres-genai:
    image: ghcr.io/a-novel/service-genai/database:v0.4.0
    networks: [api]
    environment:
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD}"
      POSTGRES_USER: "${POSTGRES_USER}"
      POSTGRES_DB: "${POSTGRES_DATABASE}"
      POSTGRES_HOST_AUTH_METHOD: scram-sha-256
      POSTGRES_INITDB_ARGS: --auth=scram-sha-256
    volumes:
      - genai-postgres-data:/var/lib/postgresql/

  migrations-genai:
    image: ghcr.io/a-novel/service-genai/jobs/migrations:v0.4.0
    depends_on:
      postgres-genai: { condition: service_healthy }
    environment:
      POSTGRES_HOST: postgres-genai
      POSTGRES_PORT: "5432"
      POSTGRES_USER: "${POSTGRES_USER}"
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD}"
      POSTGRES_DATABASE: "${POSTGRES_DATABASE}"
      POSTGRES_TLS_ENABLED: "false"
    networks: [api]

  service-genai:
    image: ghcr.io/a-novel/service-genai/grpc:v0.4.0
    ports: ["${SERVICE_GENAI_GRPC_PORT}:8080"] # the container always listens on 8080
    depends_on:
      postgres-genai: { condition: service_healthy }
      migrations-genai: { condition: service_completed_successfully }
    environment:
      POSTGRES_HOST: postgres-genai
      POSTGRES_PORT: "5432"
      POSTGRES_USER: "${POSTGRES_USER}"
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD}"
      POSTGRES_DATABASE: "${POSTGRES_DATABASE}"
      POSTGRES_TLS_ENABLED: "false"
      OPENAI_API_KEY: "${OPENAI_API_KEY}"
    networks: [api]

networks:
  api:

volumes:
  genai-postgres-data:
```

### Database image

The database image packages PostgreSQL, `pg_cron` and pgBackRest on Wolfi. Its [package manifest](./builds/database.apko.yaml) is assembled with apko during the container build; Docker and Podman users need no additional host tools. PostgreSQL keeps its standard `POSTGRES_*` initialization variables and its volume at `/var/lib/postgresql`. On first start it schedules the retention purge in the `POSTGRES_DB` database (see [CONTRIBUTING](./CONTRIBUTING.md#retention-purge)). Backup scheduling and repository credentials remain the operator's responsibility.

Start the image on a fresh volume. A data directory written by the earlier Debian-based image is not portable: the libc, collation and extension environment changed. To keep its data, take a logical dump and restore it into the new database.

Wolfi's package repository rolls forward. Retain published service images for recovery rather than relying on an old package manifest remaining rebuildable indefinitely.

### Configuration

Every variable is read from the process environment. Names can be globally prefixed with `SERVICE_GENAI_ENV_PREFIX`, which avoids collisions when another project embeds this service. Set `POSTGRES_HOST` to use the discrete connection fields; without it, the service reads `POSTGRES_DSN`.

| Name                   | Description                                                                                                                   | Images                    |
| ---------------------- | ----------------------------------------------------------------------------------------------------------------------------- | ------------------------- |
| `POSTGRES_HOST`        | PostgreSQL hostname or IP address. Selects the discrete connection fields. **Required for deployments.**                      | all                       |
| `POSTGRES_PORT`        | PostgreSQL port. Defaults to `5432`.                                                                                          | all                       |
| `POSTGRES_USER`        | PostgreSQL login role. **Required when `POSTGRES_HOST` is set.**                                                              | all                       |
| `POSTGRES_PASSWORD`    | PostgreSQL login password. **Required when `POSTGRES_HOST` is set; inject it as a secret.**                                   | all                       |
| `POSTGRES_DATABASE`    | PostgreSQL database name. **Required when `POSTGRES_HOST` is set.**                                                           | all                       |
| `POSTGRES_TLS_ENABLED` | Encrypt the PostgreSQL connection. Defaults to `true`; disable only when another trusted boundary protects the database link. | all                       |
| `POSTGRES_DSN`         | Connection URL, read only when `POSTGRES_HOST` is empty. Local development uses it.                                           | all                       |
| `OPENAI_API_KEY`       | OpenAI credential. **Required; inject it as a secret.** The server refuses to start without it. No caller holds one.          | `grpc`, `standalone-grpc` |

<details>
<summary>Optional configuration (OpenAI connector, checks, retention, gRPC, connection pool, OpenTelemetry)</summary>

OpenAI connector (images `grpc`, `standalone-grpc`). The endpoint must meet the [provider requirements](./docs/providers.md), which a conformance check verifies. The OpenAI SDK also reads `OPENAI_ORG_ID`, `OPENAI_PROJECT_ID` and `OPENAI_CUSTOM_HEADERS` from the environment, never prefixed; leave them unset unless the account needs them.

| Name              | Description             | Default                      |
| ----------------- | ----------------------- | ---------------------------- |
| `OPENAI_BASE_URL` | Responses API endpoint. | `https://api.openai.com/v1/` |

Checks and sweep (images `grpc`, `standalone-grpc`). [Generation checks](./docs/operations/generation-checks.md) explains how they move a generation forward.

| Name               | Description                                                                                                            | Default |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------- | ------- |
| `CHECK_INTERVAL`   | How long a check stays fresh. However often callers poll, a generation reaches the provider at most once per interval. | `2s`    |
| `SWEEP_INTERVAL`   | How often the sweep runs, and how stale a generation must be for it. At most `5m`.                                     | `1m`    |
| `SWEEP_BATCH_SIZE` | Generations one sweep pass checks.                                                                                     | `50`    |
| `MAX_ATTEMPTS`     | Provider calls a generation gets when a call fails retryably. Each one is paid.                                        | `2`     |
| `RETENTION`        | How long a settled generation, its attempts and its request key survive before the purge deletes them.                 | `6h`    |

gRPC server:

| Name                    | Description                                                                                                               | Default |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------- | ------- |
| `GRPC_PORT`             | Port the server listens on.                                                                                               | `8080`  |
| `GRPC_PING`             | Refresh interval for the server's internal health check.                                                                  | `5s`    |
| `GRPC_TIMEOUT_SHUTDOWN` | Graceful shutdown budget. Keep it under the orchestrator's kill delay: Cloud Run sends SIGKILL ten seconds after SIGTERM. | `8s`    |

Database connection pool (server images). The limits are **per process**, so the database's `max_connections` has to cover every replica plus the migration job; the stock `postgres` default is 100.

| Name                      | Description                               | Default |
| ------------------------- | ----------------------------------------- | ------- |
| `POSTGRES_MAX_OPEN_CONNS` | Maximum open connections to the database. | `20`    |
| `POSTGRES_MAX_IDLE_CONNS` | Maximum connections kept open while idle. | `20`    |

Planned downtime (server images):

| Name             | Description                                                                                                                                                                                                                  | Default |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `DOWNTIME_START` | When a planned downtime starts, in RFC 3339. From then until it is removed, even past the announced end, the server answers `UNAVAILABLE` except for gRPC health and echo, starts without the database, and the sweep idles. |         |

Logs and tracing — OpenTelemetry supports a stdout and a Google Cloud exporter (all server images):

| Name                | Description                                                                    | Default         |
| ------------------- | ------------------------------------------------------------------------------ | --------------- |
| `OTEL`              | Enable OTel tracing; the variables below pick the exporter.                    | `false`         |
| `GCLOUD_PROJECT_ID` | Google Cloud project ID. When set, switches logs and the OTel exporter to GCP. |                 |
| `APP_NAME`          | Application name attached to traces and logs.                                  | `service-genai` |

</details>

## Using the client package

The Go client is what a consuming service imports. The snippet below is the **minimum viable call**; the full surface is what your editor's intellisense and [pkg.go.dev](https://pkg.go.dev/github.com/a-novel/service-genai) are for.

```bash
go get github.com/a-novel/service-genai
```

```go
package main

import (
	"context"
	"log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	servicegenai "github.com/a-novel/service-genai/pkg/go"
)

func main() {
	ctx := context.Background()

	// In production, swap insecure.NewCredentials() for a TLS or mTLS credential — the
	// server has no application-layer auth, so transport security is the only thing
	// protecting it from a network adversary.
	client, err := servicegenai.NewClient(
		"service-genai:8080",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	resp, err := client.Status(ctx, &servicegenai.StatusRequest{})
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("postgres: %s", resp.GetPostgres().GetStatus())
}
```

## Running locally

The `standalone-grpc` image bundles the migration job with the server, so a single container brings the service up against an empty database. It is a development convenience: a production deployment runs migrations as their own job, so a failed migration stops the rollout instead of restarting a server.

```bash
a-novel run start service-genai/grpc
eval "$(a-novel run env service-genai)"

grpcurl --plaintext localhost:${SERVICE_GENAI_GRPC_PORT} list
```

Working on the code itself starts with [CONTRIBUTING.md](./CONTRIBUTING.md).

## Contributing

Platform setup and the day-to-day commands live in the [developer onboarding guide](https://github.com/a-novel-kit/.github/blob/master/README.md). What is specific to this service is in [CONTRIBUTING.md](./CONTRIBUTING.md).
