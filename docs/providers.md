# Providers

A provider is reached through a connector: the code that authenticates against it, owns its endpoints, writes the fields an execution mode needs over the caller's payload, and reads the provider's status and errors. The caller builds the payload in the provider's own format, so a provider is never swapped behind a caller's back: moving to another one is a change in the caller.

A connector offers a mode by implementing its interface. The OpenAI connector offers the Async mode, through the [Responses API](https://developers.openai.com/api/reference/resources/responses).

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

A provider offers the Streaming mode by reading an operation's events after a cursor. OpenAI streams only a background call created with `stream: true`, and only for 5 minutes after its creation; past that, the result is read by polling. Leaving a streamed creation before the provider reports `response.in_progress` can leave the call queued for good, an open OpenAI bug: the connector holds the creation stream until then, at most 30 seconds, and a check stops a streamed call still queued 5 minutes after its start. The connector issues the stream's GET itself, because openai-go's `GetStreaming` sends a body the API refuses ([openai-go#660](https://github.com/openai/openai-go/issues/660)).

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

The conformance check builds the connectors exactly as the server does, from the same variables, and runs the OpenAI connector's Async mode against every requirement it can observe. Run it before changing the connector or the account it reaches. It calls the provider and costs a few cents, so it runs only when `PROVIDER_CONFORMANCE` is `true`:

```bash
PROVIDER_CONFORMANCE=true OPENAI_API_KEY="<key>" go test ./cmd/grpc -run TestNewConnectors -v -timeout 30m
```

The check plays a caller: it sends its own payload, with `store: false` and a strict output schema. It starts a call and expects an id before the model runs, polls it to completion, and reads it again five minutes later. It cancels a running call and a finished one, whose result must come back intact, and expects a request for an unknown model to be rejected with the provider's error body. The whole run takes about six minutes.
