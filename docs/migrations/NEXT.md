# Upgrading to NEXT

The sweep runs only on its own schedule: `POST /sweep` and `SWEEP_TICKER` are gone.

## Do I need to act?

**Required** for operators who call `POST /sweep` from a scheduler: delete the scheduler. The sweep server now sweeps every `SWEEP_INTERVAL` on its own, as it did with `SWEEP_TICKER=true`, so run it where it keeps its CPU between requests.

**No** for callers: the gRPC API is unchanged.

## Delete the sweep scheduler

The sweep's REST server no longer serves `POST /sweep`. It answers only the `/ping` and `/healthcheck` probes, and sweeps every `SWEEP_INTERVAL` on its own. The `SWEEP_TICKER` variable is gone: the server ignores it, so remove it from the deployment when convenient.

```yaml
# before
sweep-genai:
  environment:
    SWEEP_TICKER: "true"

# after
sweep-genai:
  environment: {}
```

Delete any scheduler that posted to `/sweep`, such as a Cloud Scheduler job. A run on SIGTERM starts no new check and waits for those it started, a minute at most, so keep at least 75 seconds between SIGTERM and SIGKILL.

The sweep server's REST defaults return to golib's: `REST_TIMEOUT_REQUEST` is `1m`, `REST_TIMEOUT_WRITE` is `30s`, and `REST_CORS_ALLOWED_METHODS` is `HEAD,GET,POST,PUT,PATCH,DELETE`. Its routes only read, so an override set for the sweep's answer can go.
