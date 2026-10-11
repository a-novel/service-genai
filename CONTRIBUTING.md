# Contributing to service-genai

The shared architecture, layers, and conventions live in the [service & architecture concepts](https://github.com/a-novel/.github/blob/master/CONTRIBUTING.md); this file covers only what's specific to the GenAI service. Platform setup and day-to-day commands are in the [developer onboarding guide](https://github.com/a-novel-kit/.github/blob/master/README.md).

Read the [README](./README.md) first — it covers what the service does and how operators run it.

---

## Quick local interactions

Once the service is up (`a-novel run start service-genai/grpc`), the gRPC server listens on `${SERVICE_GENAI_GRPC_PORT}`. The port is allocated by the `a-novel` daemon; inject it into your shell with `eval "$(a-novel run env service-genai)"`. The server registers gRPC reflection, so `grpcurl` needs no proto files; the contracts are documented in [`internal/models/proto/anovel/genai/v0/`](./internal/models/proto/anovel/genai/v0/).

### Health

```bash
# Liveness
grpcurl -plaintext localhost:${SERVICE_GENAI_GRPC_PORT} grpc.health.v1.Health/Check

# Dependency check (Postgres and queue inspection)
grpcurl -plaintext localhost:${SERVICE_GENAI_GRPC_PORT} anovel.genai.v0.StatusService/Status
```

The notification server (`a-novel run start service-genai/rest`) listens on `${SERVICE_GENAI_REST_PORT}`. No provider reaches a local stack, so polls and the sweep settle its generations.

```bash
curl localhost:${SERVICE_GENAI_REST_PORT}/ping
```

### Generations

The payload and each attempt's response are `bytes` fields, so `grpcurl` takes and prints them base64-encoded. The local stack reads `OPENAI_API_KEY` from the environment it starts in, with a placeholder when it is unset (see [`builds/podman-compose.yaml`](./builds/podman-compose.yaml)); with the placeholder, every generation fails with OpenAI's authentication error as its response.

```bash
# Any UUID: the calling service supplies the owner, and this service trusts it.
OWNER_ID=$(uuidgen)

# The provider's own request, built by the caller.
PAYLOAD=$(printf '%s' '{"model":"<model>","input":"Say hello.","store":false}' | base64 | tr -d '\n')

# The purpose must be declared in internal/config/caps.config.yaml.
GENERATION_ID=$(
  grpcurl -plaintext \
    -d "{\"owner_id\":\"$OWNER_ID\",\"purpose\":\"studio.generation\",\"provider\":\"openai\",\"payload\":\"$PAYLOAD\"}" \
    localhost:${SERVICE_GENAI_GRPC_PORT} \
    anovel.genai.v0.GenerationSubmitService/GenerationSubmit |
    jq -r '.generation.id'
)
```

```bash
# Poll until the status settles.
grpcurl -plaintext -d "{\"id\":\"$GENERATION_ID\",\"owner_id\":\"$OWNER_ID\"}" \
  localhost:${SERVICE_GENAI_GRPC_PORT} \
  anovel.genai.v0.GenerationGetService/GenerationGet

# Decode the last attempt's response once it has settled.
grpcurl -plaintext -d "{\"id\":\"$GENERATION_ID\",\"owner_id\":\"$OWNER_ID\"}" \
  localhost:${SERVICE_GENAI_GRPC_PORT} \
  anovel.genai.v0.GenerationGetService/GenerationGet |
  jq -r '.generation.attempts[-1].response' | base64 -d | jq

# Stop it.
grpcurl -plaintext -d "{\"id\":\"$GENERATION_ID\",\"owner_id\":\"$OWNER_ID\"}" \
  localhost:${SERVICE_GENAI_GRPC_PORT} \
  anovel.genai.v0.GenerationCancelService/GenerationCancel
```

---

## Service-specific concepts

### The boundary

**This service never learns a caller's domain.** It executes an opaque provider payload for an owner, a purpose and a user tier, and keeps the provider's raw answers and the usage read off them. Building the request and reading the output belong to the caller. A concept like a story, a step or an engine appearing anywhere in this repository means the boundary has moved.

### Retention purge

The `database` image schedules the [retention](./README.md#configuration) purge as a `pg_cron` job in its init SQL, [`builds/database.sql`](./builds/database.sql). Every minute, it deletes up to 10,000 settled generations whose `expires_at` has passed, and their payloads and attempts with them, so a backlog left by missed runs drains in short transactions. The Usage Records stay: they hold no content and no key to the purged rows.

The job lives in the init SQL because `postgrestest.RunDBTest` clones a database with no `cron` schema, where a migration calling `cron.schedule` fails every data-access test.

**A bring-your-own-Postgres deployment must schedule it itself**, with the statement in [`builds/database.sql`](./builds/database.sql). Preload `pg_cron`, point `cron.database_name` at the service's database, and set `cron.use_background_workers = on`, as the image does: under SCRAM authentication, a job that opens its own connection fails without a password.

### Provider retention

`store` asks the provider to keep the response after the call ends. OpenAI holds a stored response for about thirty days: it can be read back with `GET /v1/responses/{id}`, and a later request can reference it through `previous_response_id` to continue a conversation without resending its history. The field belongs to the caller's payload, [like the rest of the request](./README.md#what-it-does). With `store: false`, the answers a caller needs are kept only here, in `generation_attempts`, for the retention window.

Re-attaching does not depend on `store`: a finished background response stays retrievable for a while either way (OpenAI documents about ten minutes), and the sweep reaches it well inside that window, as the [provider requirements](./docs/providers.md#async-mode-requirements) detail. A re-attach failing with a not-found on an operation that should still be running means that window has shrunk. A 404 settles the generation as failed, so a wrong bound costs one result.

### Transactions

A check settles or requeues a generation, and records the attempt's answer and its Usage Record, in one transaction. Its provider calls run outside any transaction, as the [`transaction.Transactor` contract](https://pkg.go.dev/github.com/a-novel-kit/golib/transaction#Transactor) requires.

---

## Questions?

[Open an issue](https://github.com/a-novel/service-genai/issues) — include logs and environment details.
