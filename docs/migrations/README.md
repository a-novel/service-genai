# Migration guides

- [v0.11.0](v0.11.0.md): Deploy only once v0.10 runs everywhere: the migrations drop the generation columns and the payload table it stopped using.
- [v0.10.0](v0.10.0.md): Send caps on every submit and drop `user_tier`; remove the `rest` server and `OPENAI_WEBHOOK_SECRET`; stop shipping a caps file; deploy everywhere before the next release.
- [v0.9.0](v0.9.0.md): Resend failed generations by their failure class; send each user's caps; bound your polling; remove `CHECK_INTERVAL` and `MAX_ATTEMPTS`.
- [v0.7.0](v0.7.0.md): Read a generation's times as `Timestamp`s and the queue's age as a `Duration`.
- [v0.5.0](v0.5.0.md): Submit a provider and its payload; send `store: false` yourself; read each attempt's raw response and its usage; poll instead of watching; recreate the database; schedule the sweep.
- [v0.4.0](v0.4.0.md): Handle unhealthy readiness checks as gRPC errors.
