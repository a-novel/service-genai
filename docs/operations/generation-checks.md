# Generation checks

No process holds a generation. A **check** looks at one generation and applies whatever transition is due, as a single conditional write on the row. Concurrent checks are safe: whichever commits first wins, and the others match nothing. A check that dies midway is redone by the next one.

## What triggers a check

- `GenerationSubmit` checks the generation it just created, which starts its provider call.
- `GenerationGet` checks a generation nobody checked within `CHECK_INTERVAL`. Concurrent polls elect one checker, so a generation reaches the provider at most once per interval however many callers poll it.
- A provider's notification that a call ended checks the generation running it, through the `rest` server. A notification that arrives before the call's id is recorded finds no generation, and the next poll or sweep settles it.
- The `sweep` server checks the unsettled generations nobody checked within half of `SWEEP_INTERVAL`, each time a scheduler posts to `/sweep`. A call takes batches of `SWEEP_BATCH_SIZE` while each comes back full, and starts no check its deadline could not wait out. Overlapping calls take disjoint batches.

The sweep exists for generations whose caller stopped polling. The provider keeps a finished result it does not store only for a while, and only a check frees the concurrency slot of a generation nobody follows.

## What a check does

| Generation                                      | Transition                                                                                                                                                                                             |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Its provider has no connector any more          | Settle `failed`: nothing can run or read its calls.                                                                                                                                                    |
| Pending, due, not cancelled                     | Record start intent, send the payload through the connector, record the call's id, and stop the call if its owner asked to cancel meanwhile.                                                           |
| Provider call known                             | Ask the provider once (cancel it if requested). Settle with its answer when terminal, or requeue a retryable failure while attempts remain, no cancel is requested and the generation is not streamed. |
| Start intent without an id, older than a minute | Settle `failed`: the provider may have accepted the call, so its outcome is unknown. Nothing restarts it.                                                                                              |
| Pending with cancellation requested             | Settle `cancelled`.                                                                                                                                                                                    |

A streamed generation is one provider call: its stream's cursor counts that call's events, and a fresh call numbers its own from zero. A retryable failure therefore settles it, and its caller resends it to run it again. Its call is recorded the moment the provider names it, before the creation stream's hold ends, so a process stopped during the hold still tracks the call.

## When a call goes wrong

| Situation                                                                                 | What follows                                                                                                                                                       |
| ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| A start that never left, was rate-limited, met a conflict, or was refused for credentials | The attempt is given back and the start retried 7.5 to 22.5 seconds later, for up to ten minutes after the attempt's first start. Past that, the generation fails. |
| A call that failed retryably                                                              | A fresh call starts after the same random delay.                                                                                                                   |
| A request the provider rejects, an exhausted quota included                               | Nothing is retried. The generation fails with the provider's error body for the caller to read.                                                                    |
| A start whose outcome is ambiguous                                                        | The attempt ends rather than risking a second paid call. The generation holds its owner's slot in its purpose for 15 minutes.                                      |
| A call accepted after its generation settled                                              | The call is cancelled at the provider.                                                                                                                             |
| A streamed call the provider still reports queued 5 minutes after its start               | The call is cancelled, since a creation stream left early can leave it queued for good.                                                                            |
| A call still running 2 hours after its start                                              | The call is cancelled, whether its owner asked to or not. The generation fails with what it consumed.                                                              |
| A stop that fails, or that the provider has not applied yet                               | The next checks ask again for 10 minutes from when the stop was first wanted. Then the generation fails and holds its owner's slot in its purpose for 15 minutes.  |
| A read the provider refuses for credentials                                               | The read is retried: the key is this service's configuration and says nothing about the call.                                                                      |
| A call the provider no longer holds                                                       | The generation fails, its result lost.                                                                                                                             |
| A response larger than 24 MiB                                                             | The response is never read. The generation fails and holds its owner's slot in its purpose for 15 minutes.                                                         |
| An answer the database keeps refusing to record                                           | The generation fails without it, 2 hours and 10 minutes after the call's start, when a call that cannot be stopped is given up on too.                             |

The retry delay is drawn at random, so the starts a burst turned away together come back spread out. A start the provider never accepts fails once its retries run out, so it cannot hold a concurrency slot forever.

A held slot covers a call the provider may still run while nothing tracks or meters it, so a resend cannot multiply that spend. The hold counts against the generation's purpose only: the global concurrency and the owner's other purposes stay free.

## Deploying and recovering

Nothing needs draining. A start already sent runs detached from shutdown until the process exits. The orchestrator kills it ten seconds after SIGTERM on Cloud Run, and a sweep's server exits two seconds earlier, when its drain ends. A provider answers a start well within that, so the start is not cut between the provider accepting the call and its id reaching the database. Every write the start makes after that carries its own deadline.

A sweep stops taking generations once shutdown begins, so it starts no call the kill could cut, and answers when the checks it started end. A replica that dies mid-check leaves a row the next poll or sweep picks up. A stream open on a stopping replica ends with `UNAVAILABLE`, and its caller resumes it on another replica, after the last event received.

The losses that remain are rare. Each settles its generation `failed` with an unknown outcome, and its answer goes unrecorded:

- A hard kill (SIGKILL, out of memory, host loss) between the provider accepting a call and its id being written.
- A start the provider is still answering when the orchestrator kills a stopping process.
- A database too unavailable to record the id or to read the row back.
- A start whose caller gave up, answered by the provider after the start's deadline. A server has CPU only while it handles a request, so the abandoned check waits for the server's next request. A streamed submit or poll given a deadline of at least 35 seconds is never given up mid-start.

A write of the id that got no answer is tried once more, since the database may still commit it. A call the generation is then seen not to carry is stopped. One the generation cannot be read for is left to run.

## Providers and keys

A generation runs on the provider named in its submission, through that provider's connector, until it settles. Its payload is in that provider's format, so it never moves to another provider.

Rotating the OpenAI key within one account changes `OPENAI_API_KEY` alone. Calls in flight stay readable, and nothing is lost. Pointing the connector at another account or endpoint abandons the calls in flight, since the new credentials cannot read the old account's operations. Their next check settles them as lost, and a resend pays again. Make that change while nothing runs, or accept the cost. A deployment that drops a connector fails the generations still running on its provider.
