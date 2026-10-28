# GenAI service

Executes the platform's AI provider calls durably, pays for each once, and returns what the provider answered.

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

A provider call can take minutes, costs real money, and must be paid for once. The caller decides what a call executes, and this service decides how it executes. The service never reads the requests it runs or the answers it returns.

The caller holds no connection while the provider works. It submits the call, then polls for the result. The call outlives the caller's request, a restart of either side, and a deploy.

Five rules shape the contract.

**The caller builds the request.** A submission names its `purpose`, in the caller's own vocabulary, and a provider, `openai` today. Its payload is the provider's own request body, as raw JSON. The caller owns the model, the prompt, the output schema, the limits and the privacy fields such as `store`. The provider's connector writes only the fields the execution mode needs, and authenticates with the platform's only provider credential. Calls run in the Async mode: the provider runs the call on its side while the caller polls. A submission can also ask to `stream`. `GenerationStream` then forwards the provider's events as they come, for the first 5 minutes of the call, while polls and checks work on it as on any other.

**A resend is a replay.** The service derives each generation's key from its owner, purpose, provider, `stream` flag, payload and `variant`. A caller that lost its connection or crashed sends the same request again and gets the same generation back, running or finished. The caller keeps no key of its own. Formatting and the order of top-level fields leave the key unchanged. Nested fields keep their order in the key, since a provider can read meaning into it, such as a schema's property order. A failed or cancelled generation is not served again: resending its request runs it from scratch. To ask for another result of a request that succeeded, send it with the next `variant`.

**A crash re-attaches instead of paying again.** The service records the provider's identifier for a call the moment the call starts, and every later check reads it back. No process holds a generation, so a restart or a deploy loses nothing. The next poll or sweep picks up the call already paid for. Every accepted call is followed until it settles, whether anyone polls it or not.

**The provider's answers come back as sent.** Each generation lists every attempt that reached an answer, retried and cancelled ones included, with the provider's raw response. That response holds the output and the usage. A request the provider rejected comes back with the provider's error body. A generation that holds no finished response says why in the service's own words. The purge deletes a generation and its responses after `RETENTION`. A permanent Usage Record of every answered attempt stays: who it ran for, what ran, what it consumed and when, with no content.

**Usage is capped per purpose and user tier.** The [caps file](./internal/config/caps.config.yaml) is built into the image, so every change to it is reviewed. It declares each purpose a caller may submit and the token limits of each user tier over rolling windows. It also caps how many generations a user may run at once, per purpose and across all of them. Its comments explain every key. Once a user reaches a limit, a submit that would start a new generation answers `RESOURCE_EXHAUSTED`, naming the limit. An admitted call always finishes, even past its cap, and a resend of a running generation always attaches to it.

Callers are other services, which reach the generation API over **gRPC** on the internal network. Once service tokens are configured, the server admits only the callers they allow. The optional `rest` server is public and serves no generation. It only receives the providers' notifications that their calls ended.

## Deploying

The service runs as published OCI images plus a PostgreSQL database. All state lives in Postgres, so the server scales to as many replicas as you need. Replicas coordinate through conditional writes on each generation row.

> **OpenTofu modules are the planned canonical deployment path.** Until they land, deploy the images with any container orchestrator — the composition below is the reference for which images to run, how they wire together, and the environment they expect.

| Image                           | Role                                                                        |
| ------------------------------- | --------------------------------------------------------------------------- |
| `service-genai/grpc`            | The generation API. Internal network only.                                  |
| `service-genai/sweep`           | Checks the generations nobody polled, on a schedule. Private.               |
| `service-genai/rest`            | Receives the provider's notifications that a call ended. Public.            |
| `service-genai/jobs/migrations` | One-shot schema migration job; runs to completion before the server starts. |
| `service-genai/database`        | PostgreSQL with `pg_cron` and pgBackRest, or bring your own Postgres.       |
| `service-genai/standalone-grpc` | Server plus migrations in one image. Local development only.                |

Pin every image to the same release tag, from the [latest release](https://github.com/a-novel/service-genai/releases/latest). A production deployment starts `database` and runs the migrations job to completion. It then runs any number of `grpc` replicas, the `sweep` server, and optionally `rest` replicas. Provide the required [configuration](#configuration) through the orchestrator.

A caller controls the whole payload, including the model and the provider's built-in tools. Grant invocation to calling services only, and restrict the OpenAI key to the Responses endpoints.

Every server does its work inside a request, so none needs CPU outside of one. A `sweep` server with `SWEEP_TICKER` set is the exception. Each needs outbound access to the provider's API. A check its caller abandons resumes only with the server's next request, and a streamed start waits for the provider to begin the call. Give a streamed generation's submit and polls a deadline of at least 35 seconds.

```yaml
services:
  postgres-genai:
    image: ghcr.io/a-novel/service-genai/database:v0.7.4
    networks: [api]
    environment:
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD}"
      POSTGRES_USER: "${POSTGRES_USER}"
      POSTGRES_DB: "${POSTGRES_DATABASE}"
      POSTGRES_HOST_AUTH_METHOD: scram-sha-256
      POSTGRES_INITDB_ARGS: --auth=scram-sha-256
    volumes:
      - genai-postgres-data:/var/lib/postgresql/
    # Podman ignores the image's own check, so the dependents' wait needs this one.
    healthcheck:
      test: ["CMD", "pg_isready"]
      interval: 1s
      timeout: 5s
      retries: 10

  migrations-genai:
    image: ghcr.io/a-novel/service-genai/jobs/migrations:v0.7.4
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
    image: ghcr.io/a-novel/service-genai/grpc:v0.7.4
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

  sweep-genai:
    image: ghcr.io/a-novel/service-genai/sweep:v0.7.4
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

  # Any scheduler fits; this one calls the sweep every minute.
  sweep-schedule-genai:
    image: docker.io/library/alpine:3.24.2
    depends_on: [sweep-genai]
    command: ["sh", "-c", "while sleep 60; do wget -qO- --post-data= http://sweep-genai:8080/sweep; done"]
    networks: [api]

networks:
  api:

volumes:
  genai-postgres-data:
```

### The sweep

Each poll checks its generation with the provider. The `sweep` server checks the generations nobody polled. It captures a result before the provider discards it, stops a stuck call, and frees a user's concurrency slot.

Run it privately, and have a scheduler `POST /sweep` every `SWEEP_INTERVAL`. On Google Cloud, use Cloud Scheduler with an OIDC token for the only identity allowed to invoke it. A call checks batch after batch and answers within two minutes. The server checks as many generations at once as its connection pool has connections, eight at most, however many calls overlap.

On a platform that keeps the server's CPU between requests, such as Kubernetes, set `SWEEP_TICKER` instead. The server then sweeps every `SWEEP_INTERVAL` on its own and needs no scheduler. On SIGTERM, a run starts no new check and waits for those it started, a minute at most. Give the process at least 75 seconds between SIGTERM and SIGKILL. From `DOWNTIME_START` on, the server starts no run.

### Provider notifications

The `rest` image checks a generation the moment its provider reports the call ended, instead of on the next poll or sweep. It serves `POST /webhooks/{provider}`, `/ping` and `/healthcheck`. It reuses the database and the migrations, and needs the same variables as `grpc` plus `OPENAI_WEBHOOK_SECRET`.

To enable it for OpenAI:

1. Expose the `rest` server publicly.
2. In the OpenAI project's settings, create a webhook endpoint pointing at `https://<host>/webhooks/openai`. Subscribe it to `response.completed`, `response.failed`, `response.cancelled` and `response.incomplete`.
3. Store the endpoint's signing secret as `OPENAI_WEBHOOK_SECRET`.

Polls and the sweep keep working for a notification that never comes. Once notifications flow, raise `CHECK_INTERVAL` so polls stop reaching the provider.

### Database image

The database image packages PostgreSQL, `pg_cron` and pgBackRest on Wolfi. apko assembles its [package manifest](./builds/database.apko.yaml) during the container build, pinned by its [lock file](./builds/database.apko.lock.json), so an unchanged image rebuilds to the same digest. Docker and Podman users need no additional host tools. PostgreSQL keeps its standard `POSTGRES_*` initialization variables and its volume at `/var/lib/postgresql`. On first start, it installs `pg_cron` in the `POSTGRES_DB` database, where the migrations job schedules the [retention purge](./CONTRIBUTING.md#retention-purge). Backup scheduling and repository credentials remain the operator's responsibility.

Wolfi's package repository rolls forward, so an old package manifest may stop rebuilding. Keep the published service images for recovery.

### Configuration

Every variable is read from the process environment. `SERVICE_GENAI_ENV_PREFIX` prefixes every name, which avoids collisions when another project embeds this service.

| Name                    | Description                                                                                                                           | Images      |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ----------- |
| `POSTGRES_HOST`         | PostgreSQL hostname or IP address. Selects the discrete connection fields. **Required for deployments.**                              | all         |
| `POSTGRES_PORT`         | PostgreSQL port. Defaults to `5432`.                                                                                                  | all         |
| `POSTGRES_USER`         | PostgreSQL login role. **Required when `POSTGRES_HOST` is set.**                                                                      | all         |
| `POSTGRES_PASSWORD`     | PostgreSQL login password. **Required when `POSTGRES_HOST` is set; inject it as a secret.**                                           | all         |
| `POSTGRES_DATABASE`     | PostgreSQL database name. **Required when `POSTGRES_HOST` is set.**                                                                   | all         |
| `POSTGRES_TLS_ENABLED`  | Encrypt the PostgreSQL connection. Defaults to `true`; disable only when another trusted boundary protects the database link.         | all         |
| `POSTGRES_DSN`          | Connection URL, read only when `POSTGRES_HOST` is empty. Local development uses it.                                                   | all         |
| `OPENAI_API_KEY`        | OpenAI credential. **Required; inject it as a secret.** The server refuses to start without it. No caller holds one.                  | all servers |
| `OPENAI_WEBHOOK_SECRET` | Signing secret of the OpenAI webhook endpoint, `whsec_…`. **Inject it as a secret.** Without it, OpenAI's notifications answer `404`. | `rest`      |

<details>
<summary>Optional configuration</summary>

OpenAI connector (all servers). The endpoint must meet the [provider requirements](./docs/providers.md), which a conformance check verifies. The OpenAI SDK also reads `OPENAI_ORG_ID`, `OPENAI_PROJECT_ID` and `OPENAI_CUSTOM_HEADERS` from the environment, never prefixed. Leave them unset unless the account needs them.

| Name              | Description             | Default                      |
| ----------------- | ----------------------- | ---------------------------- |
| `OPENAI_BASE_URL` | Responses API endpoint. | `https://api.openai.com/v1/` |

Checks (all servers) and sweep (`sweep`). [Generation checks](./docs/operations/generation-checks.md) explains how they move a generation forward.

| Name               | Description                                                                                                                                                          | Default |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `CHECK_INTERVAL`   | How long a check stays fresh. However often callers poll, a generation reaches the provider at most once per interval.                                               | `2s`    |
| `SWEEP_INTERVAL`   | How often the sweep runs. Each run checks every generation nobody checked within half of it. At most `5m`.                                                           | `1m`    |
| `SWEEP_BATCH_SIZE` | Generations one batch of a sweep takes, from 1 to 100. A sweep takes another batch while each comes back full.                                                       | `50`    |
| `SWEEP_TICKER`     | Run the sweep every `SWEEP_INTERVAL` from the server itself, besides `POST /sweep`. The server then needs CPU between requests.                                      | `false` |
| `MAX_ATTEMPTS`     | Provider calls a generation gets when a call fails retryably, 1 or 2, so a generation's responses fit one message. Each one is paid. A streamed generation gets one. | `2`     |
| `RETENTION`        | How long a settled generation, its attempts and its request key survive before the purge deletes them. At least `15m`.                                               | `6h`    |

gRPC server:

| Name                    | Description                                                                                                                  | Default |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------- | ------- |
| `GRPC_PORT`             | Port the server listens on.                                                                                                  | `8080`  |
| `GRPC_TIMEOUT_SHUTDOWN` | Graceful shutdown budget. With the 4 seconds of telemetry flushes after it, it fits Cloud Run's 10 seconds before `SIGKILL`. | `6s`    |

Service tokens (image `grpc`). Without `SERVICE_TOKEN_ISSUER`, the server takes every call. With it, the server refuses a call unless its `X-Service-Authorization` header carries a token from an allowed caller. The gRPC health service stays open for probes:

| Name                     | Description                                                                                                                                                                                                                                                                 | Default |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `SERVICE_TOKEN_ISSUER`   | Issuer of the service tokens the server accepts, such as a GKE cluster's `https://container.googleapis.com/v1/projects/<project>/locations/<location>/clusters/<cluster>`. The server reads its keys through its OpenID discovery document, so it needs egress to its host. |         |
| `SERVICE_TOKEN_AUDIENCE` | Audience the server answers to. Each caller's projected token names it.                                                                                                                                                                                                     |         |
| `SERVICE_TOKEN_CALLERS`  | Service accounts allowed to call, comma-separated, each as `system:serviceaccount:<namespace>:<name>`. In production, the GenAI smoke check.                                                                                                                                |         |

REST servers (images `rest` and `sweep`). Every notification comes from the provider's few addresses, so the rate limit is sized for the provider's bursts.

| Name                        | Description                                                                                                                                   | Default         |
| --------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- | --------------- |
| `REST_PORT`                 | Port the server listens on.                                                                                                                   | `8080`          |
| `REST_MAX_REQUEST_SIZE`     | Maximum request body size, in bytes. A notification carries an event, not the result.                                                         | `16384` (16KiB) |
| `REST_CLIENT_IP_HEADER`     | Header a trusted proxy sets to the visitor IP. Set it only when every request comes through that proxy.                                       | TCP peer        |
| `REST_LIVENESS_PATH`        | Path that still answers during a planned downtime and skips the rate limit. The servers answer their ping at `/ping`, so keep it there.       | `/ping`         |
| `REST_RATE_LIMITS_ENABLED`  | Rate-limit each visitor on every route but `/ping`. Turn off only in test stacks.                                                             | `true`          |
| `REST_RATE_LIMIT`           | Requests each visitor sends to each route, as `<requests>/<window>`.                                                                          | `500/1s`        |
| `REST_CORS_ALLOWED_METHODS` | CORS allowed methods, comma-separated.                                                                                                        | `HEAD,GET,POST` |
| `REST_TIMEOUT_READ`         | Read timeout.                                                                                                                                 | `15s`           |
| `REST_TIMEOUT_READ_HEADER`  | Header read timeout.                                                                                                                          | `3s`            |
| `REST_TIMEOUT_WRITE`        | Write timeout. Keep it above the request timeout, or a sweep's answer is lost.                                                                | `2m15s`         |
| `REST_TIMEOUT_IDLE`         | Idle keep-alive timeout.                                                                                                                      | `60s`           |
| `REST_TIMEOUT_REQUEST`      | Per-request timeout. A sweep starts no check this deadline could not wait out, and a check takes a minute at most, so keep it above a minute. | `2m`            |
| `REST_TIMEOUT_SHUTDOWN`     | Graceful shutdown budget. With the 4 seconds of telemetry flushes after it, it fits Cloud Run's 10 seconds before `SIGKILL`.                  | `6s`            |

Memory (server images). A request holds a few copies of what it carries: a submit its payload, up to 16 MiB, and a read its responses, up to 24 MiB each. Twenty concurrent submits of the largest payloads peak near 1.6 GiB on one `grpc` instance. Give it 2 GiB with `GOMEMLIMIT` near `1600MiB`, or lower its request concurrency.

Database connection pool (server images). The limits are **per process**. Keep `POSTGRES_MAX_OPEN_CONNS` × the maximum replica count × 2, which covers a rollout's overlap, plus the migration job, under the database's `max_connections`. The server's sessions also give up on the database side: a statement after 9 seconds, a lock wait after 5, and a transaction left idle after 30.

| Name                      | Description                               | Default |
| ------------------------- | ----------------------------------------- | ------- |
| `POSTGRES_MAX_OPEN_CONNS` | Maximum open connections to the database. | `20`    |
| `POSTGRES_MAX_IDLE_CONNS` | Maximum connections kept open while idle. | `20`    |

Planned downtime (server images):

| Name             | Description                                  | Default |
| ---------------- | -------------------------------------------- | ------- |
| `DOWNTIME_START` | When a planned downtime starts, in RFC 3339. |         |

From `DOWNTIME_START` until the variable is removed, even past the announced end, the servers answer `503` / `UNAVAILABLE`. Ping, gRPC health and echo still answer, and the servers start without the database. A request already running still finishes, a sweep up to `REST_TIMEOUT_REQUEST` later, so stop the database only once that has passed. The provider delivers the notifications it could not send once the downtime ends. Calls already sent keep running at the provider. OpenAI keeps a finished background response it does not store for about 10 minutes. A call that finishes more than 10 minutes before the downtime ends therefore settles failed, its result lost. Keep a downtime short while generations run.

Logs and tracing (all server images). OpenTelemetry exports to stdout, or over OTLP/HTTP once an endpoint is set.

| Name                             | Description                                                                                                      | Default         |
| -------------------------------- | ---------------------------------------------------------------------------------------------------------------- | --------------- |
| `OTEL`                           | Enable OTel tracing; the variables below pick the exporter.                                                      | `false`         |
| `OTEL_EXPORTER_OTLP_ENDPOINT`    | Base URL of the OTLP/HTTP backend. When set, traces and logs export there, and the servers write no request log. |                 |
| `OTEL_EXPORTER_OTLP_HEADERS`     | Headers each export sends, such as the backend's key. Inject it as a secret.                                     |                 |
| `OTEL_EXPORTER_OTLP_COMPRESSION` | `gzip` compresses each export.                                                                                   |                 |
| `OTEL_SERVICE_NAME`              | Service name every span and log record carries.                                                                  |                 |
| `APP_NAME`                       | Application name attached to traces and logs.                                                                    | `service-genai` |

Error reporting (server images). With `SENTRY_DSN` set, the servers send each server fault to Sentry: a `5xx`, a gRPC `INTERNAL` or `UNAVAILABLE`, and a panic. Without it, they report nothing.

| Name                 | Description                                                                                                                    | Default |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------ | ------- |
| `SENTRY_DSN`         | Client key of the service's Sentry project. **Inject it as a secret.**                                                         |         |
| `SENTRY_RELEASE`     | Release the servers run, as `service-genai@<version>`, the name release CI gives it in Sentry. **Required with `SENTRY_DSN`.** |         |
| `SENTRY_ENVIRONMENT` | Deployment name, such as `production`. **Required with `SENTRY_DSN`.**                                                         |         |

</details>

## Using the client package

The Go client is what a consuming service imports. Its full surface is on [pkg.go.dev](https://pkg.go.dev/github.com/a-novel/service-genai).

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

	// The server has no application-layer auth, so transport security alone protects it from a
	// network adversary. In production, replace insecure.NewCredentials() with a TLS or mTLS
	// credential.
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

The `standalone-grpc` image bundles the migration job with the server, so a single container brings the service up against an empty database. A production deployment runs migrations as their own job instead, so a failed migration stops the rollout rather than restarting a server. Nothing sweeps a local stack: polls settle its generations.

```bash
a-novel run start service-genai/grpc
eval "$(a-novel run env service-genai)"

grpcurl --plaintext "localhost:${SERVICE_GENAI_GRPC_PORT}" list
```

Without the CLI, the repository's compose file runs a target on its own. Enable the target's profile and set the host ports it publishes. The `sweep` target serves `POST /sweep` on `SWEEP_PORT`.

```bash
POSTGRES_PORT=5432 GRPC_PORT=8080 podman compose -f builds/podman-compose.yaml --profile grpc up -d
```

## Contributing

Platform setup and the day-to-day commands live in the [developer guide](https://a-novel-kit.github.io/stack/). What is specific to this service is in [CONTRIBUTING.md](./CONTRIBUTING.md).
