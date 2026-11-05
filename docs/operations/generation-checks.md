# Generation checks

No process holds a generation. A **check** looks at one generation and applies whatever transition is due, as a single conditional write on the row. Concurrent checks are safe: whichever commits first wins, and the others match nothing. A check that dies midway is redone by the next one.

After its insert, a run writes its `generations` row twice: the provider's call id once the provider accepts the call, and the settle. A check that finds nothing to settle writes nothing, however often it runs.

## What triggers a check

- `GenerationSubmit` inserts the generation with its start, then sends the payload it holds in memory and records the call's id. The service never stores the payload, so no other check can send it.
- `GenerationGet` checks every unsettled generation it reads, so a poll made after the provider finished returns the settled generation. The caller bounds how often it polls.
- The `sweep` server checks every unsettled generation, each time a scheduler posts to `/sweep`, and every `SWEEP_INTERVAL` when `SWEEP_TICKER` is set. One run leads at a time across every replica: it holds a PostgreSQL advisory lock on a connection of its own, and a run that finds it taken checks nothing. The leader reads batches of `SWEEP_BATCH_SIZE` in id order while each comes back full, and starts no check its deadline could not wait out.

The sweep exists for generations whose caller stopped polling. The provider keeps a finished result it does not store only for a while, and only a check frees the concurrency slot of a generation nobody follows.

Every age a check measures, such as how long a call has run, comes from the database clock at the read that fed it.

## What a check does

| Generation                                        | Transition                                                                                                                                     |
| ------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| Its provider has no connector any more            | Settle `failed`: nothing can run or read its calls.                                                                                            |
| Just inserted by the submit                       | Send the payload through the connector, record the call's id, and stop the call if its owner asked to cancel meanwhile.                        |
| Provider call known                               | Ask the provider once (cancel it if requested). Settle with its answer when terminal, whatever the answer.                                     |
| Start without an id, older than three minutes     | Settle `failed`, `OUTCOME_UNKNOWN`: the provider may have accepted the call. Nothing sends it again.                                           |
| Pending with no start, left by an earlier release | Settle `failed`, `NOT_STARTED`, or `cancelled` when its owner asked to cancel. Its payload is out of reach, so the caller resends the request. |

A generation is one provider call. Its call is recorded the moment the provider names it, before a streamed creation's hold ends, so a process stopped during the hold still tracks the call.

## When a call goes wrong

The service never sends a call again. A generation settles on its first outcome with a failure class, and the caller decides whether to resend.

| Situation                                                                                 | What follows                                                                                                                                                           |
| ----------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A start that never left, was rate-limited, met a conflict, or was refused for credentials | The generation fails, `NOT_STARTED`. Nothing ran.                                                                                                                      |
| A request the provider rejects, an exhausted quota included                               | The generation fails, `REJECTED`, with the provider's error body for the caller to read.                                                                               |
| A start whose outcome is ambiguous                                                        | The generation fails, `OUTCOME_UNKNOWN`, rather than risking a second paid call. It holds its owner's slot in its purpose for 15 minutes.                              |
| A call that failed, retryable or not                                                      | The generation fails, `PROVIDER_FAILED`, with the provider's answer.                                                                                                   |
| A write of the call id that waits on a lock                                               | The write is sent again after a pause that doubles from 250 milliseconds up to 5 seconds, until two minutes after the start.                                           |
| A call accepted after its generation settled                                              | The call is cancelled at the provider.                                                                                                                                 |
| A streamed call the provider still reports queued 5 minutes after its start               | The call is cancelled, since a creation stream left early can leave it queued for good. The generation fails, `PROVIDER_FAILED`.                                       |
| A call still running 2 hours after its start                                              | The call is cancelled, whether its owner asked to or not. The generation fails, `PROVIDER_FAILED`, with what it consumed.                                              |
| A stop that fails, or that the provider has not applied yet                               | The next checks ask again for 10 minutes from when the stop was first wanted. Then the generation fails, `OUTCOME_UNKNOWN`, and holds its owner's slot for 15 minutes. |
| A read the provider refuses for credentials                                               | The read is asked again by the next check: the key is this service's configuration and says nothing about the call.                                                    |
| A call the provider no longer holds                                                       | The generation fails, `PROVIDER_FAILED`, its result lost.                                                                                                              |
| A response larger than 24 MiB                                                             | The response is never read. The generation fails, `OUTCOME_UNKNOWN`, and holds its owner's slot in its purpose for 15 minutes.                                         |
| An answer the database keeps refusing to record                                           | The generation fails without it, `PROVIDER_FAILED`, 2 hours and 10 minutes after the call's start, when a call that cannot be stopped is given up on too.              |

A held slot covers a call the provider may still run while nothing tracks or meters it, so a resend cannot multiply that spend. The hold counts against the concurrency of the generation's purpose only: the owner's other purposes stay free.

## Deploying and recovering

Nothing needs draining. A start already sent runs detached from shutdown until the process exits. Kubernetes kills it 30 seconds after SIGTERM, its default grace period, long after a server's drain ends at 6 seconds. A provider answers a start well within that, so the start is not cut between the provider accepting the call and its id reaching the database. Every write the start makes after that carries its own deadline.

A sweep stops taking generations once shutdown begins, and answers when the checks it started end. Its leader releases the advisory lock as the run ends, and the database releases it with the session if the process dies. With `SWEEP_TICKER`, the server exits once its run's checks end, or when Kubernetes kills it at 30 seconds. A replica that dies mid-check leaves a row the next poll or sweep picks up. A stream open on a stopping replica ends with `UNAVAILABLE`, and its caller resumes it on another replica, after the last event received.

The losses that remain are rare. Each settles its generation `failed`, `OUTCOME_UNKNOWN`, and its answer goes unrecorded:

- A hard kill (SIGKILL, out of memory, host loss) between the provider accepting a call and its id being written.
- A start the provider is still answering when the orchestrator kills a stopping process.
- A database too unavailable to record the id within two minutes, or to read the row back.
- A start whose caller gave up, answered by the provider after the start's deadline. A server has CPU only while it handles a request, so the abandoned check waits for the server's next request. A streamed submit given a deadline of at least 35 seconds is never given up mid-start.

A write of the id that got no answer may still commit, so the next write waits behind it on the row. A call the generation is then seen not to carry is stopped. One the generation cannot be read for is left to run.

## Providers and keys

A generation runs on the provider named in its submission, through that provider's connector, until it settles. Its payload is in that provider's format, so it never moves to another provider.

Rotating the OpenAI key within one account changes `OPENAI_API_KEY` alone. Calls in flight stay readable, and nothing is lost. Pointing the connector at another account or endpoint abandons the calls in flight, since the new credentials cannot read the old account's operations. Their next check settles them as lost, and a resend pays again. Make that change while nothing runs, or accept the cost. A deployment that drops a connector fails the generations still running on its provider.
