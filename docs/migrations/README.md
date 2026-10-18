# Migration guides

| Release             | Consumer action                                                                                                                                                                       |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [v0.5.0](v0.5.0.md) | Submit a provider and its payload; send `store: false` yourself; read each attempt's raw response and its usage; poll instead of watching; recreate the database; schedule the sweep. |
| [v0.4.0](v0.4.0.md) | Handle unhealthy readiness checks as gRPC errors.                                                                                                                                     |
