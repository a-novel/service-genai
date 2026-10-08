# GenAI service

Every AI generation on the platform runs here: the call to the provider, the record that survives a crash without paying twice, and what each call consumed.

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

A generation takes minutes, costs real money, and must never be paid for twice. A caller states what it wants: a Tier, its instructions, the user's input and an output schema. This service picks the model for that Tier, calls the provider, survives its own restarts without re-billing, and hands back a document conforming to the schema, with what each provider call consumed.

Callers keep their domain. Prompt assembly stays with them, so instructions, input and schema arrive already built, but nothing about talking to a provider, recovering a crashed call or counting tokens is written twice.

Five things shape the contract:

**Callers never name a model.** They pick a Tier (`FAST`, `BALANCED`, `DEEP`), and the provider configuration binds each Tier to a model and a reasoning effort. `TierList` reports each Tier's input and output token ceilings, so a caller sizes its request without knowing the model. Switching provider is a configuration change that edits no caller, and work in flight restarts on the new provider.

**A resend is a replay.** The service derives each generation's key from the request and its owner, so a caller that lost its connection or crashed sends the same request again and gets the same generation back, running or finished. It keeps no key of its own. A failed or cancelled generation is not served again: resending its request runs it from scratch. To ask for another result of a request that succeeded, send it with the next `variant`.

**A crash re-attaches instead of re-paying.** The provider's own identifier for an in-flight operation is recorded the moment the call starts, and every later check reads it back. No process holds a generation, so a restart or a deploy loses nothing: the next poll or sweep picks up the operation already paid for.

**A failure says why.** A failed generation carries the kind of failure that ended it (refused, incomplete, invalid request, or failed) and the cause in this service's own words. The caller decides what to do; raw provider text stays in server logs.

**Usage is reported, not kept.** Each generation lists what every provider call consumed, failed ones included, with the model and effort that actually ran. Callers keep the long-term record; this service purges usage with the generation it describes.

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

Pin every image to the same release tag — see the [latest release](https://github.com/a-novel/service-genai/releases/latest). A production deployment runs `database`, then the migrations job to completion, then any number of `grpc` replicas. Provide `POSTGRES_HOST`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DATABASE` and `PROVIDER_API_KEY` through the orchestrator; store the password and the API key as secrets.

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
      PROVIDER_API_KEY: "${PROVIDER_API_KEY}"
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
| `PROVIDER_API_KEY`     | Provider credential. **Required; inject it as a secret.** The server refuses to start without it. No caller holds one.        | `grpc`, `standalone-grpc` |

<details>
<summary>Optional configuration (provider, checks, retention, gRPC, connection pool, OpenTelemetry)</summary>

Provider (images `grpc`, `standalone-grpc`). The defaults run on OpenAI; [switching provider](./docs/operations/generation-checks.md#switching-provider) sets every one of them, the key included, and raises the epoch. The endpoint must meet the [provider requirements](./docs/providers.md), which a conformance check verifies before the switch. The OpenAI SDK also reads `OPENAI_ORG_ID`, `OPENAI_PROJECT_ID` and `OPENAI_CUSTOM_HEADERS` from the environment, unprefixed; leave them unset unless the account needs them.

| Name                | Description                                                                                                                                                                                                                                                        | Default                                                |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------ |
| `PROVIDER_NAME`     | Identifies the provider on usage records. Changed with the account or endpoint, not on key rotation.                                                                                                                                                               | `openai`                                               |
| `PROVIDER_EPOCH`    | Orders provider configurations. Raise it on every switch, and never lower it: work started under a lower epoch restarts on this one, and work under a higher one waits for it.                                                                                     | `1`                                                    |
| `PROVIDER_BASE_URL` | Responses API endpoint.                                                                                                                                                                                                                                            | `https://api.openai.com/v1/`                           |
| `PROVIDER_TIERS`    | JSON object binding each of `fast`, `balanced` and `deep` to `{"model", "reasoningEffort", "maxInputTokens", "maxOutputTokens"}`. The effort is passed as is; `maxInputTokens` is the model's published maximum input. Every Tier needs a model and both ceilings. | [OpenAI bindings](./internal/config/tiers.openai.json) |

Checks and sweep (images `grpc`, `standalone-grpc`). [Generation checks](./docs/operations/generation-checks.md) explains how they move a generation forward.

| Name               | Description                                                                                                            | Default |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------- | ------- |
| `CHECK_INTERVAL`   | How long a check stays fresh. However often callers poll, a generation reaches the provider at most once per interval. | `2s`    |
| `SWEEP_INTERVAL`   | How often the sweep runs, and how stale a generation must be for it. At most `5m`.                                     | `1m`    |
| `SWEEP_BATCH_SIZE` | Generations one sweep pass checks.                                                                                     | `50`    |
| `MAX_ATTEMPTS`     | Provider calls a generation gets when a call fails retryably. Each one is paid.                                        | `2`     |
| `RETENTION`        | How long a settled generation, its usage and its request key survive before the purge deletes them.                    | `6h`    |

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
