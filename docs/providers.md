# Providers

A connector is the code that reaches a provider. It authenticates against the provider, owns its endpoints, writes the fields an execution mode needs over the caller's payload, and reads the provider's status and errors. The caller builds the payload in the provider's own format, so moving to another provider is a change in the caller.

A connector offers a mode by implementing its interface. The OpenAI connector offers the Async and Streaming modes, through the [Responses API](https://developers.openai.com/api/reference/resources/responses), and the Notifications mode once given its webhook signing secret.

## Async mode requirements

| Requirement                                                                            | Why the service needs it                                                                                                                      |
| -------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| Create, retrieve by id, and cancel an operation                                        | The connector's three operations.                                                                                                             |
| An operation id returned before the model runs                                         | Crash safety: the id is recorded before anything else, so a restarted check re-attaches instead of paying again. OpenAI's `background: true`. |
| Asynchronous runs available without provider-side retention                            | A caller that sets `store: false` keeps its prose out of a 30-day provider copy, and the call stays recoverable.                              |
| A finished operation stays retrievable for at least five minutes                       | The sweep reads an unpolled result within `SWEEP_INTERVAL`, which is capped at five minutes. OpenAI kept one for over three hours.            |
| Cancelling a finished operation returns its result, or a read still does               | A cancel that lost a race with completion keeps the paid result.                                                                              |
| A JSON error body on a rejected request                                                | The caller built the request, so the provider's reason is what tells it what to fix.                                                          |
| A finished operation names its model and reasoning effort, and reports its token usage | Every answered call leaves a Usage Record of what ran and what it consumed, which caps sum.                                                   |

The OpenAI connector writes `background: true` and `stream` over the payload's own values, and forwards everything else unchanged. `stream` is `false` unless the caller asked to stream the generation. Background mode reorders a `json_schema`'s properties before the model runs, so a caller cannot use property order to make the model write one field before another.

## Streaming mode

A provider offers the Streaming mode by reading an operation's events after a cursor. OpenAI streams only a background call created with `stream: true`, and only for 5 minutes after its creation. Past that, the result is read by polling.

An open OpenAI bug can leave a streamed call queued for good when its creation stream is left before the provider reports `response.in_progress`. The connector holds the creation stream until then, for 30 seconds at most. It reports the call's id as soon as the provider names it, so the call is recorded before the hold ends. A check stops a streamed call still queued 5 minutes after its start.

A streamed generation is one call, since a fresh call numbers its events from zero. A retryable failure settles it instead of starting a fresh call. The connector issues the stream's GET itself, because openai-go's `GetStreaming` sends a body the API refuses ([openai-go#660](https://github.com/openai/openai-go/issues/660)).

## Notifications mode

A provider offers the Notifications mode by posting a signed message when one of its operations ends. The connector verifies the message and names the operation it concerns, or reports it invalid or irrelevant to the service. The `rest` server then checks the generation carrying that operation. Nothing provider-specific leaves the connector, so the server serves every provider at `/webhooks/{provider}`.

OpenAI follows the [Standard Webhooks](https://www.standardwebhooks.com/) specification. Each event is signed with the endpoint's secret, delivered at least once, in no guaranteed order, and retried for up to 72 hours until it is answered with a `2xx`. The connector verifies events with openai-go's `Webhooks.Unwrap` and acts on `response.completed`, `response.failed`, `response.cancelled` and `response.incomplete`. An event carries only the response id, so the check retrieves the result as a poll would. A duplicate or late event finds the generation already settled.

## Who conforms

As of October 2026:

| Provider                                                                         | Verdict                                                                                                                                                         |
| -------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| OpenAI                                                                           | Conforms. The OpenAI connector runs on it.                                                                                                                      |
| Azure OpenAI (`/openai/v1/`)                                                     | Background mode [requires `store=true`](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/responses), so its calls cannot avoid provider retention. |
| AWS Bedrock (Mantle)                                                             | Unverified. Background mode is "subject to data retention settings".                                                                                            |
| vLLM (self-hosted)                                                               | Background mode only with its response store enabled, and that store lives in memory on one replica.                                                            |
| OpenRouter, Groq, xAI, Ollama, and the Gemini and Anthropic compatibility layers | No background mode. They can offer only a synchronous mode.                                                                                                     |

## Checking a provider

The conformance check builds the connectors from the variables the server reads, all but the webhook secret. It runs the OpenAI connector's Async and Streaming modes against every requirement it can observe. Run it before changing the connector or the account it reaches. It calls the provider and costs a few cents, so it runs only when `PROVIDER_CONFORMANCE` is `true`. Paste the API key at the prompt:

```bash
read -rs OPENAI_API_KEY
export OPENAI_API_KEY
PROVIDER_CONFORMANCE=true go test ./internal/lib/connectors -run TestOpenAI/Conformance -v -timeout 30m
```

The check plays a caller, with its own payload, `store: false` and a strict output schema. It starts a call and expects an id before the model runs, polls it to completion, and reads it again five minutes later. It streams another call, expecting it named while queued and its events in order up to the terminal one. It cancels a running call and a finished one, whose result must come back intact. It expects a request for an unknown model to be rejected with the provider's error body. The whole run takes about six minutes.
