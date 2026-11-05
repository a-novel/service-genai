# Migration guides

| Release             | Consumer action                                                                                                                                                                       |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [v0.9.0](v0.9.0.md) | Resend failed generations by their failure class; send each user's caps; bound your polling; remove `CHECK_INTERVAL` and `MAX_ATTEMPTS`.                                              |
| [v0.7.0](v0.7.0.md) | Read a generation's times as `Timestamp`s and the queue's age as a `Duration`.                                                                                                        |
| [v0.5.0](v0.5.0.md) | Submit a provider and its payload; send `store: false` yourself; read each attempt's raw response and its usage; poll instead of watching; recreate the database; schedule the sweep. |
| [v0.4.0](v0.4.0.md) | Handle unhealthy readiness checks as gRPC errors.                                                                                                                                     |
