# Upgrading to NEXT

The sweep runs only on its own schedule: `POST /sweep` and `SWEEP_TICKER` are gone. A generation now always carries its start intent, the servers read only the discrete `POSTGRES_*` fields, and the unused `pkg/go/mocks` package is gone.

## Do I need to act?

**Required** for operators who call `POST /sweep` from a scheduler: delete the scheduler. The sweep server now sweeps every `SWEEP_INTERVAL` on its own, as it did with `SWEEP_TICKER=true`, so run it where it keeps its CPU between requests.

**Required** for operators who set `POSTGRES_DSN` without `POSTGRES_HOST`: set the discrete `POSTGRES_*` fields instead.

**Required** for operators still on a release before v0.9.0: upgrade through v0.11 first. The migration refuses a generation without a start intent, which only those releases recorded.

**Required** for Go callers that import `github.com/a-novel/service-genai/pkg/go/mocks`: mock your own interface instead.

**No** for other callers: the gRPC API is unchanged.

## Delete the sweep scheduler

The sweep's REST server no longer serves `POST /sweep`. It answers only the `/ping` and `/healthcheck` probes, and sweeps every `SWEEP_INTERVAL` on its own. The `SWEEP_TICKER` variable is gone: the server ignores it, so remove it from the deployment when convenient.

```yaml
sweep-genai:
  environment:
    SWEEP_TICKER: "true" # delete this line
```

Delete any scheduler that posted to `/sweep`, such as a Cloud Scheduler job. A run on SIGTERM starts no new check and waits for those it started, a minute at most, so keep at least 75 seconds between SIGTERM and SIGKILL.

The sweep server's REST defaults return to golib's: `REST_TIMEOUT_REQUEST` is `1m`, `REST_TIMEOUT_WRITE` is `30s`, and `REST_CORS_ALLOWED_METHODS` is `HEAD,GET,POST,PUT,PATCH,DELETE`. Its routes only read, so an override set for the sweep's answer can go.

## Replace POSTGRES_DSN with the discrete fields

The servers and the migrations job no longer read `POSTGRES_DSN`. A deployment that set it, and not `POSTGRES_HOST`, sets each part of that URL in its own variable instead: `POSTGRES_HOST`, `POSTGRES_PORT` (default `5432`), `POSTGRES_USER`, `POSTGRES_PASSWORD` (inject it as a secret), `POSTGRES_DATABASE`, and `POSTGRES_TLS_ENABLED=false` where the URL said `sslmode=disable`.

A deployment that already sets `POSTGRES_HOST` needs nothing: the DSN was read only without it.

## Check for generations without a start intent

The migration sets `generations.start_requested_at` to `NOT NULL`. Every submit records it since v0.9.0, and the sweep settled the generations earlier releases left without one. The purge deleted them once their retention ended. A row that survived fails the migration, before the new servers deploy, and the previous release keeps serving. To check beforehand:

```sql
SELECT
  count(*)
FROM
  generations
WHERE
  start_requested_at IS NULL;
```

A non-zero count means a pre-v0.9.0 release ran within the last `RETENTION`. Wait for the sweep to settle those generations and for the purge to delete them, then run the migrations job again.

## Mock your own client interface

`pkg/go/mocks` is removed: no repository imported it. Declare the interface your code consumes, with only the methods it calls, and generate its mock in your own repository.

```go
// before
client := pkgmocks.NewMockClient(t)

// after: in your package
type genaiClient interface {
	GenerationGet(ctx context.Context, req *servicegenai.GenerationGetRequest, opts ...grpc.CallOption) (*servicegenai.GenerationGetResponse, error)
}
```

## Notes

The down migration drops the constraint again and keeps every row. A usage-cap refusal's status message now reads `usage cap reached: purpose "<name>" <limit>`, such as `input tokens over 5h`; its `QuotaFailure` and `RetryInfo` details are unchanged.
