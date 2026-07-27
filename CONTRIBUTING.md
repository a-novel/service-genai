# Contributing to service-genai

This file covers only what is specific to **service-genai**. For service-level contribution shared across every service — the architecture, the layers, the conventions — start with the [service & architecture concepts](https://github.com/a-novel/.github/blob/master/CONTRIBUTING.md). Platform setup and day-to-day commands are in the [developer onboarding guide](https://github.com/a-novel-kit/.github/blob/master/README.md).

---

## Where the service is

The scaffold is in place; the generation API is being built. [a-novel/.github#245](https://github.com/a-novel/.github/issues/245) holds the design and the task order — read it before adding anything here, because the boundary it draws is the point of the service.

The `item` resource is inherited placeholder wiring, not a feature. It exercises every layer and the whole test rail end to end, which is why it survives the scaffold; it is replaced wholesale by the real schema, not extended.

**The one rule that must not erode: this service never learns a caller's domain.** It receives finished text and an output schema, and returns structured output and a cost. A concept like a story, a step or an engine appearing anywhere in this repository means the boundary has moved and the service is on its way to being the thing it replaced.

---

## Running it locally

Start the server and load its ports into your shell:

```bash
a-novel run start service-genai/grpc
eval "$(a-novel run env service-genai)"
```

Check it is alive:

```bash
grpcurl -plaintext localhost:${SERVICE_GENAI_GRPC_PORT} StatusService/Status
```

There is no REST surface: callers are other services on the internal network, so a second transport would be a second contract to keep in step with no consumer asking for it.

---

## Transactions

Two or more writes that must land together are wrapped in a `transaction.Transactor`, taken as a constructor dependency by the service that needs one and injected in `cmd`. It names no database, so business code says "these writes are one unit" without knowing what stores them:

```go
// internal/core
type SomeService struct {
	dao        SomeServiceDao
	transactor transaction.Transactor
}

err := service.transactor.WithinTx(ctx, func(ctx context.Context) error {
	// every data-access call made with this ctx is part of one transaction
})

// cmd
service := core.NewSomeService(daoSomething, postgres.NewTransactor(nil))
```

Nothing in this service uses one yet, because no operation writes twice — the `item` resource is single-write throughout. The convention is here rather than demonstrated because wrapping a single write in a transaction is noise, and a scaffold that shows it teaches every service copied from it to do the same.

**Pass the callback's `ctx` down, not the outer one.** Data-access objects resolve their database handle from the context, and the transaction is installed on the context the callback receives. An inner call given the outer context runs on the connection pool and commits on its own, while the surrounding block still reports success. That is not hypothetical: it is what a sibling service did in four operations for months, with a green build the whole time.

Two rules follow, and the shared library's documentation is the contract for both:

- **Never call an external service inside `WithinTx`.** An open transaction holds a pooled connection for its whole lifetime; pinning one for the length of a third-party call exhausts the pool and blocks vacuuming. Persist what the call needs, close the transaction, make the call, then open a new transaction to record the result. `postgres.InTx(ctx)` reports whether a transaction is open, so a data-access object that makes an outbound call can refuse rather than rely on the convention holding.
- **A nested `WithinTx` joins the transaction in progress**, so a rollback anywhere discards the whole outermost unit of work — including work the outer caller believed was already safe. Nesting is legal; it should be deliberate. A nested call also never sees its own `sql.TxOptions`, so an operation needing a specific isolation level has to be the outermost transaction.

Unit-test a service that takes a transactor with `transactiontest.NewTransactor`, which runs the callback inline, or `NewFailingTransactor` to cover the path where the unit of work never opens — asserting the dependencies are never reached is how a test proves the writes are inside the scope rather than merely near it. A test that needs a real rollback needs a real database: use `postgres.RunDBTest`, never `RunTransactionalTest`, whose passthrough transaction cannot tell a working transactor from a broken one.

---

## Questions?

[Open an issue](https://github.com/a-novel/service-genai/issues) — include logs and environment details.
