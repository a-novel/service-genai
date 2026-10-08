# Generation checks

No process holds a generation. A **check** looks at one generation and applies whatever transition is due, as a single conditional write on the row. Concurrent checks are safe because whichever commits first wins and the others match nothing. A check that dies mid-way is redone by the next one.

## What triggers a check

- `GenerationSubmit` checks a generation it just created, which starts its provider call.
- `GenerationGet` checks a generation nobody checked within `CHECK_INTERVAL`. Concurrent polls elect one checker, so a generation reaches the provider at most once per interval however many callers poll it.
- The sweep checks unsettled generations nobody checked within `SWEEP_INTERVAL`, in batches of `SWEEP_BATCH_SIZE`. Replicas take disjoint batches. It exists for generations whose caller stopped polling: the provider keeps a finished result only for a while when it does not store it.

## What a check does

| Generation                                        | Transition                                                                                                                             |
| ------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Pending, due, not cancelled                       | Record start intent, send the call, record its id.                                                                                     |
| Provider call known                               | Ask the provider once (cancel it if requested). Settle with usage when terminal, or requeue a retryable failure while attempts remain. |
| Start intent without an id, older than 30 seconds | Settle `failed` with `generation outcome unknown`. Nothing restarts it.                                                                |
| Pending with cancellation requested               | Settle `cancelled`.                                                                                                                    |

A rate-limited start costs nothing, so its attempt is given back and the start retried later. A start whose outcome is ambiguous may have been accepted, so the attempt ends rather than risking a second paid call. A call accepted after its generation settled is cancelled at the provider.

## Deploying and recovering

Nothing needs draining. Every check finishes inside the shutdown budget, and the start call runs detached from shutdown so it cannot be cut between the provider accepting it and its id reaching the database. A replica that dies mid-check leaves a row the next poll or sweep picks up.

The one loss left is a hard kill (SIGKILL, out of memory, host loss) in the second between the provider accepting a call and its id being written. That generation settles as `generation outcome unknown` and its usage goes unrecorded.
