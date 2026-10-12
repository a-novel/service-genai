# Generation checks

No process holds a generation. A **check** looks at one generation and applies whatever transition is due, as a single conditional write on the row. Concurrent checks are safe because whichever commits first wins and the others match nothing. A check that dies mid-way is redone by the next one.

## What triggers a check

- `GenerationSubmit` checks a generation it just created, which starts its provider call.
- `GenerationGet` checks a generation nobody checked within `CHECK_INTERVAL`. Concurrent polls elect one checker, so a generation reaches the provider at most once per interval however many callers poll it.
- A provider's notification that a call ended checks the generation running it, through the `rest` server.
- The sweep checks unsettled generations nobody checked within `SWEEP_INTERVAL`, in batches of `SWEEP_BATCH_SIZE`. Replicas take disjoint batches. It exists for generations whose caller stopped polling: the provider keeps a finished result only for a while when it does not store it.

## What a check does

| Generation                                      | Transition                                                                                                                                                                                             |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Its provider has no connector any more          | Settle `failed`: nothing can run or read its calls.                                                                                                                                                    |
| Pending, due, not cancelled                     | Record start intent, send the payload through the connector, record the call's id.                                                                                                                     |
| Provider call known                             | Ask the provider once (cancel it if requested). Settle with its answer when terminal, or requeue a retryable failure while attempts remain, no cancel is requested and the generation is not streamed. |
| Start intent without an id, older than a minute | Settle `failed`: the provider may have accepted the call, so its outcome is unknown. Nothing restarts it.                                                                                              |
| Pending with cancellation requested             | Settle `cancelled`.                                                                                                                                                                                    |

A start that never left, was rate-limited, met a conflict or was refused for this service's credentials costs nothing: its attempt is given back and the start retried later, for up to ten minutes after the attempt's first start. Past that, the generation fails, so a request the provider never accepts cannot hold a concurrency slot. A request the provider rejects, an exhausted quota included, is not retried: the generation fails with the provider's error body for the caller to read. A start whose outcome is ambiguous may have been accepted, so the attempt ends rather than risking a second paid call, and the generation keeps its owner's concurrency slot for 15 minutes: the provider may still run a call nothing tracks or meters. A call accepted after its generation settled is cancelled at the provider. A streamed call the provider still reports queued 5 minutes after its start is cancelled the same way, since a creation stream left early can leave it queued for good. A call still running 2 hours after its start is cancelled too, whether its owner asked to cancel it or not, and the generation fails with what it consumed. A stop that fails, or that the provider has not applied yet, is asked again by the next checks; 10 minutes past the ceiling, the generation fails all the same, so it never holds a slot forever, but keeps its owner's slot for 15 minutes, as an ambiguous start does: the call may still run.

A streamed generation is one provider call: its stream's cursor counts that call's events, and a fresh call would number its own from zero. A retryable failure therefore settles it, and its caller resends it to run it again. Its call is recorded the moment the provider names it, before the creation stream's hold ends, so a process stopped during the hold still tracks the call.

A read the provider refuses for credentials is retried too, since the key is this service's configuration and says nothing about the call. Only a call the provider no longer holds settles as lost.

## Deploying and recovering

Nothing needs draining. A stopping process launches no new sweep check, and a start already sent runs detached from shutdown, so it cannot be cut between the provider accepting the call and its id reaching the database, and every write it makes after that carries its own deadline. A replica that dies mid-check leaves a row the next poll or sweep picks up.

Two losses remain, both rare. A hard kill (SIGKILL, out of memory, host loss) in the second between the provider accepting a call and its id being written settles that generation `failed` with an unknown outcome, and its answer goes unrecorded. A database too unavailable to record the id or to read the row back does the same. A write of the id that got no answer is tried once more, since the database may still commit it; then a call the generation is seen not to carry is stopped, but one it cannot be read for is left to run.

## Providers and keys

A generation runs on the provider named in its submission, through that provider's connector, until it settles. Its payload is in that provider's format, so it never moves to another provider.

Rotating the OpenAI key within one account changes `OPENAI_API_KEY` alone: calls in flight stay readable, and nothing is lost. Pointing the connector at another account or endpoint abandons the calls in flight, since the new credentials cannot read the old account's operations: their next check settles them as lost, and a resend pays again. Make that change while nothing runs, or accept the cost. A deployment that drops a connector fails the generations still running on its provider.
