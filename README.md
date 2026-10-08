# GenAI service

Every AI generation on the platform runs here: the call to the provider, the record that survives a crash without paying twice, and the ledger of what it cost and who it was for.

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

A generation takes minutes, costs real money, and must never be paid for twice. A caller submits _what it wants generated_; this service picks the model, calls the provider, survives its own restart without re-billing, records exactly what was consumed and what it cost, and hands back the result.

Callers keep their domain and lose their worker. Prompt assembly stays with them — the text and the output schema arrive already built — but nothing about talking to a provider, recovering a crashed call, counting tokens or pricing them is written twice.

Three things shape the contract:

**Idempotency is mandatory.** Every submission carries a key, and a replay attaches to the work already in flight rather than starting a second, separately billed one. In a service whose only workload is a priced call, an unkeyed submission is a bug the API refuses rather than a default it tolerates.

**A crash re-attaches instead of re-paying.** The provider's own identifier for an in-flight operation is recorded the moment the call starts. A restarted process resumes that operation; it does not begin a new one.

**Cost is recorded, not inferred.** The provider, the model and the token breakdown are not enough to reconstruct a bill, because prices change. Each attempt writes a ledger row carrying the unit prices in force at the time and the resulting amount, so what a call cost stays true no matter what the price becomes later. Each row also carries the **purpose** it was spent on, because the platform does not monetize every AI feature the same way.

The surface is **gRPC only**. Callers are other services on the internal network, so there is no browser client and no REST API to keep in step.

## Deploying

The service runs as published OCI images plus a PostgreSQL database. All state lives in Postgres, so the server scales to as many replicas as you need; replicas share the queue through fenced claims.

> **OpenTofu modules are the planned canonical deployment path.** Until they land, deploy the images with any container orchestrator — the composition below is the reference for which images to run, how they wire together, and the environment they expect.

| Image                           | Role                                                                        |
| ------------------------------- | --------------------------------------------------------------------------- |
| `service-genai/grpc`            | The generation API, its worker and its reaper. Internal network only.       |
| `service-genai/jobs/migrations` | One-shot schema migration job; runs to completion before the server starts. |
| `service-genai/database`        | PostgreSQL with `pg_cron` and pgBackRest — or bring your own Postgres.      |
| `service-genai/standalone-grpc` | Server plus migrations in one image. Local development only.                |

Pin every image to the same release tag — see the [latest release](https://github.com/a-novel/service-genai/releases/latest). A production deployment runs `database`, then the migrations job to completion, then any number of `grpc` replicas. Provide `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DATABASE` and `OPENAI_API_KEY` through the orchestrator; store the password and the API key as secrets.

The worker runs inside the `grpc` process and keeps calling the provider between requests. Give each replica CPU outside of requests (on Cloud Run, CPU always allocated with at least one instance) and outbound access to the provider's API.

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
| `OPENAI_API_KEY`       | Provider credential. **Required for generations; inject it as a secret.** No caller holds one.                                | `grpc`, `standalone-grpc` |

<details>
<summary>Optional configuration (provider, worker, retention, gRPC, connection pool, OpenTelemetry)</summary>

Provider (images `grpc`, `standalone-grpc`):

| Name              | Description                                                                                 | Default     |
| ----------------- | ------------------------------------------------------------------------------------------- | ----------- |
| `OPENAI_BASE_URL` | Provider endpoint. Points the service at an OpenAI-compatible provider or a local stand-in. | SDK default |

Worker and reaper (images `grpc`, `standalone-grpc`). [Generation claims](./docs/operations/generation-claims.md) explains how they interact.

| Name                   | Description                                                                   | Default  |
| ---------------------- | ----------------------------------------------------------------------------- | -------- |
| `WORKER_ID`            | Identifies this replica on the claims it holds.                               | hostname |
| `WORKER_INTERVAL`      | How often the worker looks for work when the queue is empty.                  | `5s`     |
| `WORKER_LEASE`         | How long a claim holds before the reaper may recover it.                      | `5m`     |
| `WORKER_BATCH_SIZE`    | Generations processed per pass.                                               | `10`     |
| `WORKER_POLL_INTERVAL` | Wait between polls of a running provider operation.                           | `2s`     |
| `REAPER_INTERVAL`      | How often the reaper sweeps for lapsed leases.                                | `30s`    |
| `REAPER_GRACE`         | Head start a late settle gets over recovery.                                  | `30s`    |
| `REAPER_BATCH_SIZE`    | Generations recovered per sweep.                                              | `100`    |
| `RETENTION`            | How long a settled generation's content survives before the purge deletes it. | `168h`   |

gRPC server:

| Name                    | Description                                              | Default |
| ----------------------- | -------------------------------------------------------- | ------- |
| `GRPC_PORT`             | Port the server listens on.                              | `8080`  |
| `GRPC_PING`             | Refresh interval for the server's internal health check. | `5s`    |
| `GRPC_TIMEOUT_SHUTDOWN` | Graceful shutdown budget.                                | `30s`   |

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
