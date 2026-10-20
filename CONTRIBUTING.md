# Contributing to service-genai

This document is about what is specific to the GenAI service. Read the [README](./README.md) first: it covers what the service does and how operators run it. The [service and architecture concepts](https://github.com/a-novel/.github/blob/master/CONTRIBUTING.md) cover the layers every service shares, and the [developer guide](https://a-novel-kit.github.io/stack/) covers platform setup and the day-to-day commands.

---

## Quick local interactions

Start the service as the README's [Running locally](./README.md#running-locally) shows. The `a-novel` daemon allocates its gRPC port, and `eval "$(a-novel run env service-genai)"` loads it into your shell as `SERVICE_GENAI_GRPC_PORT`. The server registers gRPC reflection, so `grpcurl` needs no proto files. The contracts are documented in [`internal/models/proto/anovel/genai/v0/`](./internal/models/proto/anovel/genai/v0/).

### Health

```bash
# Liveness
grpcurl -plaintext "localhost:${SERVICE_GENAI_GRPC_PORT}" grpc.health.v1.Health/Check

# Dependency check (Postgres and queue inspection)
grpcurl -plaintext "localhost:${SERVICE_GENAI_GRPC_PORT}" anovel.genai.v0.StatusService/Status
```

The notification server, started with `a-novel run start service-genai/rest`, listens on `SERVICE_GENAI_REST_PORT`. No provider reaches a local stack, so it receives no notification.

```bash
curl "localhost:${SERVICE_GENAI_REST_PORT}/ping"
```

To sweep a local stack by hand, start the sweep server with the stack's environment:

```bash
eval "$(a-novel run env service-genai)"
REST_PORT=8090 go run ./cmd/sweep
```

Once it logs `Starting sweep server on :8090`, post to it from another shell:

```bash
curl -X POST localhost:8090/sweep
```

### Generations

The payload and each attempt's response are `bytes` fields, so `grpcurl` takes and prints them base64-encoded. The local stack reads `OPENAI_API_KEY` from the environment it starts in, and falls back to a placeholder (see [`builds/podman-compose.yaml`](./builds/podman-compose.yaml)). OpenAI refuses every start made with the placeholder. The service retries it as a fault of its own configuration, so the generation stays pending for ten minutes, then fails with no attempt.

```bash
# Any UUID: the calling service supplies the owner, and this service trusts it.
OWNER_ID=$(uuidgen)

# The provider's own request, built by the caller.
PAYLOAD=$(printf '%s' '{"model":"<model>","input":"Say hello.","store":false}' | base64 | tr -d '\n')

# The purpose must be declared in internal/config/caps.config.yaml.
GENERATION_ID=$(
  grpcurl -plaintext \
    -d "{\"owner_id\":\"$OWNER_ID\",\"purpose\":\"studio.generation\",\"provider\":\"openai\",\"payload\":\"$PAYLOAD\"}" \
    "localhost:${SERVICE_GENAI_GRPC_PORT}" \
    anovel.genai.v0.GenerationSubmitService/GenerationSubmit |
    jq -r '.generation.id'
)
```

```bash
# Poll until the status settles.
grpcurl -plaintext -d "{\"id\":\"$GENERATION_ID\",\"owner_id\":\"$OWNER_ID\"}" \
  "localhost:${SERVICE_GENAI_GRPC_PORT}" \
  anovel.genai.v0.GenerationGetService/GenerationGet

# Decode the last attempt's response once it has settled.
grpcurl -plaintext -d "{\"id\":\"$GENERATION_ID\",\"owner_id\":\"$OWNER_ID\"}" \
  "localhost:${SERVICE_GENAI_GRPC_PORT}" \
  anovel.genai.v0.GenerationGetService/GenerationGet |
  jq -r '.generation.attempts[-1].response' | base64 -d | jq

# Stop it.
grpcurl -plaintext -d "{\"id\":\"$GENERATION_ID\",\"owner_id\":\"$OWNER_ID\"}" \
  "localhost:${SERVICE_GENAI_GRPC_PORT}" \
  anovel.genai.v0.GenerationCancelService/GenerationCancel
```

---

## Service-specific concepts

### The boundary

**This service never learns a caller's domain.** It executes an opaque provider payload for an owner, a purpose and a user tier, and keeps the provider's raw answers and the usage read off them. Building the request and reading the output belong to the caller. A concept like a story, a step or an engine appearing anywhere in this repository means the boundary has moved.

### Retention purge

The [retention](./README.md#configuration) purge is the `purge_settled_generations()` function. A migration defines it and schedules it as a `pg_cron` job wherever the extension is installed, as the `database` image does. Every 10 seconds, the job deletes up to 1,000 settled generations whose `expires_at` has passed, with their payloads and attempts. It stops once those store 64 MiB. The job keeps up with 100 generations settling a second, and drains a backlog of large ones in transactions shorter than a migration's lock wait. `pg_cron` records every run and deletes none, so a second job deletes the service's runs older than a week, once a day. The Usage Records stay: they hold no content and no key to the purged rows. A change to the purge is a migration that replaces the function, and its tests run it.

The migration skips the schedule where `pg_cron` is missing, as in the databases `postgrestest.RunDBTest` clones. **A bring-your-own-Postgres deployment that ran the migrations without `pg_cron` schedules both jobs itself**, with the block of the [migration that last scheduled them](./internal/models/migrations/20261014021047_generation_purge_schedule.up.sql). Preload `pg_cron`, point `cron.database_name` at the service's database, and set `cron.use_background_workers = on`, as the image does. Under SCRAM authentication, a job that opens its own connection fails without a password.

### Migrations on live tables

Every check rewrites a row of `generations`, and some of those writes record a call the provider already accepted. A write left waiting for a lock gives up after the server's `lock_timeout`, five seconds. A call whose id is not recorded settles failed with its outcome unknown, paid for and unread. A migration on `generations` therefore holds no lock that blocks writes for more than a second or two. Follow [`write-sql`](https://github.com/a-novel-kit/stack/blob/master/.agents/skills/write-sql/SKILL.md#migrations-on-live-tables): build an index concurrently, add a constraint `NOT VALID` then validate it, and run a backfill in batches outside the migration.

### Provider retention

`store` asks the provider to keep the response after the call ends. OpenAI holds a stored response for about thirty days. It can be read back with `GET /v1/responses/{id}`, and a later request can reference it through `previous_response_id` to continue a conversation without resending its history. The field belongs to the caller's payload, [like the rest of the request](./README.md#what-it-does). With `store: false`, the answers a caller needs are kept only here, in `generation_attempts`, for the retention window.

Re-attaching does not depend on `store`. A finished background response stays retrievable for a while either way, about ten minutes as OpenAI documents it. The sweep reaches it well inside that window, as the [provider requirements](./docs/providers.md#async-mode-requirements) detail. A re-attach failing with a not-found on an operation that should still be running means that window has shrunk. A 404 settles the generation as failed, so a wrong bound costs one result.

### Transactions

A check settles or requeues a generation, and records the attempt's answer and its Usage Record, in one transaction. Its provider calls run outside any transaction, as the [`transaction.Transactor` contract](https://pkg.go.dev/github.com/a-novel-kit/golib/transaction#Transactor) requires.

---

## Questions?

[Open an issue](https://github.com/a-novel/service-genai/issues) — include logs and environment details.
