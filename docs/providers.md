# Providers

The service talks to one provider through the [Responses API](https://developers.openai.com/api/reference/resources/responses). [Switching provider](./operations/generation-checks.md#switching-provider) is a configuration change, but only to an endpoint that implements everything below. An endpoint that does not needs a second adapter ([#226](https://github.com/a-novel/service-genai/issues/226)), which is a design decision rather than a configuration entry.

## Requirements

| Requirement                                                                                            | Why the service needs it                                                                                                       |
| ------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------ |
| Responses API: create, retrieve by id, cancel                                                          | The adapter's three operations.                                                                                                |
| `background: true` returns an id before the model runs                                                 | Crash safety: the id is recorded before anything else, so a restarted check re-attaches instead of paying again.               |
| Background works with `store: false`                                                                   | The provider keeps no 30-day copy of user prose.                                                                               |
| A finished background response stays retrievable for at least five minutes                             | The sweep reads an unpolled result within `SWEEP_INTERVAL`, which is capped at five minutes. OpenAI kept one for over an hour. |
| `text.format` `json_schema` with `strict: true`, enforced by constrained decoding                      | The output contract. A schema the model only tries to follow is not enough.                                                    |
| Each Tier's model accepts its reasoning effort and `max_output_tokens`                                 | A Tier the provider rejects fails every generation sent to it.                                                                 |
| Each Tier's model has a context window of at least `max_input_tokens` plus `max_output_tokens`         | `TierList` advertises both ceilings, and a caller sizes its request against them.                                              |
| `usage` reports input, cached input, output and reasoning tokens, and the response names the model run | Each generation reports what every call consumed and what actually ran.                                                        |
| `metadata` and `safety_identifier` accepted                                                            | An orphaned call stays identifiable, and the provider attributes abuse to a hashed user rather than to the platform.           |

Output schemas stay within OpenAI's [strict-mode subset](https://developers.openai.com/api/docs/guides/structured-outputs#supported-schemas): an object root, every property required, `additionalProperties: false`, and its keyword list and size limits. The service does not validate schemas itself; a schema the provider rejects fails the generation as `INVALID_REQUEST`.

A schema's property order does not steer the output. Background mode reorders the properties before the model runs, so the output's keys follow the provider's order rather than the caller's: a field meant to be generated before another, such as an analysis before its verdict, is not guaranteed to be. Synchronous calls kept the order when tested, but the service needs background mode.

## Who conforms

As of October 2026:

| Provider                                                                         | Verdict                                                                                                                                                                                                                                                                                                                                                              |
| -------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| OpenAI                                                                           | Conforms. The committed configuration runs on it.                                                                                                                                                                                                                                                                                                                    |
| Azure OpenAI (`/openai/v1/`)                                                     | Fails. Background mode [requires `store=true`](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/responses), and its strict subset is [narrower](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/structured-outputs): 100 properties, 5 levels, no `pattern`, `format` or bounds. Usable only after deciding to accept provider retention. |
| AWS Bedrock (Mantle)                                                             | Unverified. Background mode is "subject to data retention settings".                                                                                                                                                                                                                                                                                                 |
| vLLM (self-hosted)                                                               | Background mode only with its response store enabled, and that store lives in memory on one replica.                                                                                                                                                                                                                                                                 |
| OpenRouter, Groq, xAI, Ollama, and the Gemini and Anthropic compatibility layers | Fail. Their Responses or chat-compatible endpoints have no background mode.                                                                                                                                                                                                                                                                                          |

## Checking a provider

The conformance check builds the provider exactly as the server does, from the same `PROVIDER_*` variables, and runs it against every requirement it can observe. Run it before switching provider or changing the Tier bindings. It calls the provider and costs a few cents, so it runs only when `PROVIDER_CONFORMANCE` is `true`:

```bash
PROVIDER_CONFORMANCE=true PROVIDER_API_KEY="<key>" go test ./cmd/grpc -run TestNewProvider -v -timeout 30m
```

Add the other `PROVIDER_*` variables to check a candidate rather than the committed OpenAI configuration. For each Tier, the check starts a background call and expects an id before the model runs, polls it to completion, and verifies the output against a strict reference schema. It then verifies the usage and reads the result again five minutes later. It also cancels a running call. A Tier whose model rejects its effort or output ceiling fails with the binding named and the provider's message. The whole run takes about six minutes.

The input ceiling is the one requirement the check does not exercise: proving it would mean sending about a million tokens per Tier on every run. Verify `max_input_tokens` plus `max_output_tokens` against the provider's published context window instead.
