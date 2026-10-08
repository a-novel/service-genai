# Generation checks

No process holds a generation. A **check** looks at one generation and applies whatever transition is due, as a single conditional write on the row. Concurrent checks are safe because whichever commits first wins and the others match nothing. A check that dies mid-way is redone by the next one.

## What triggers a check

- `GenerationSubmit` checks a generation it just created, which starts its provider call.
- `GenerationGet` checks a generation nobody checked within `CHECK_INTERVAL`. Concurrent polls elect one checker, so a generation reaches the provider at most once per interval however many callers poll it.
- The sweep checks unsettled generations nobody checked within `SWEEP_INTERVAL`, in batches of `SWEEP_BATCH_SIZE`. Replicas take disjoint batches. It exists for generations whose caller stopped polling: the provider keeps a finished result only for a while when it does not store it. It skips generations a newer provider epoch took over.

## What a check does

| Generation                                        | Transition                                                                                                                             |
| ------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Taken over by a newer provider epoch              | Nothing. The replicas running that epoch check it.                                                                                     |
| Started on an older provider epoch                | Discard the attempt and return the generation to pending on this epoch, without spending a retry. The next check starts it.            |
| Pending, due, not cancelled                       | Record start intent, send the call, record its id.                                                                                     |
| Provider call known                               | Ask the provider once (cancel it if requested). Settle with usage when terminal, or requeue a retryable failure while attempts remain. |
| Start intent without an id, older than 30 seconds | Settle `failed`: the provider may have accepted the call, so its outcome is unknown. Nothing restarts it.                              |
| Pending with cancellation requested               | Settle `cancelled`.                                                                                                                    |

A rate-limited start costs nothing, so its attempt is given back and the start retried later. A start whose outcome is ambiguous may have been accepted, so the attempt ends rather than risking a second paid call. A call accepted after its generation settled is cancelled at the provider.

## Deploying and recovering

Nothing needs draining. Every check finishes inside the shutdown budget, and the start call runs detached from shutdown so it cannot be cut between the provider accepting it and its id reaching the database. A replica that dies mid-check leaves a row the next poll or sweep picks up.

The one loss left is a hard kill (SIGKILL, out of memory, host loss) in the second between the provider accepting a call and its id being written. That generation settles `failed` with an unknown outcome, and its usage goes unrecorded.

## Switching provider

A switch is one revision that sets `PROVIDER_NAME`, `PROVIDER_BASE_URL`, `PROVIDER_API_KEY` and `PROVIDER_TIERS` for the new provider and raises `PROVIDER_EPOCH`. Rotating the key within one account changes `PROVIDER_API_KEY` alone and keeps the epoch, so nothing restarts.

Generations keep their id and request key across a switch. One started under a lower epoch restarts on the new provider at its next check, and its usage is recorded under the new name. The attempt number still increments, so each attempt keeps its own usage row.

During the rollout, replicas of both revisions run side by side. A replica checks only generations at or below its own epoch, and every start or restart raises a generation's epoch to the replica's own. An old replica therefore never takes back a generation a new one started, and calls are not restarted back and forth between the two providers.

The restart abandons the call on the old provider: without the old key, it can be neither read nor cancelled. It runs to completion unobserved, and its spend is not recorded here. A switch while no generation is pending or running loses nothing; otherwise, expect to pay a second time for the work in flight.
