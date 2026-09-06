# Deploying generation claim fencing

Stop every generation worker and reaper before applying the claim migration, then restart all
replicas with the updated binary. Both loops run inside the gRPC service. Drain active work first
where possible. Old replicas identify claims by hostname and cannot participate safely in the new
protocol.

Apply the paired `generation_claims` migration through the existing migration target. It adds
`claim_token`, which changes on every acquisition, and `start_requested_at`, which records that a
provider request may have been accepted. Existing pending or running generations with an inference
attempt receive Start intent conservatively; known provider IDs remain attached and resumable.

`WORKER_BATCH_SIZE` limits the number of jobs processed in a pass. Each job is claimed only when the
serial worker can execute it. `WORKER_LEASE` is renewed during active execution, including while a
provider call is blocked. Expiry is checked against the database clock. `REAPER_GRACE` delays recovery
without extending an expired worker's authority.

A cancellation observed before Start prevents paid work. Cancellation committed after the last
control check can race with provider acceptance; the worker records the operation ID and requests
provider cancellation on its next control check. A provider's terminal result determines the outcome
and usage accounting.

An expired generation with Start intent and no provider ID settles as failed with
`generation outcome unknown`. It is not automatically replaced, even when attempts remain. This also
covers a crash after intent was recorded but before the request was sent. Investigate the provider
operation using the existing generation and attempt metadata before authorizing any replacement.
Known provider operations retain their inference attempt when another worker resumes observation.

Stop all workers and reapers before rolling back. The down migration removes both ownership and Start
intent evidence; restore or reconcile uncertain generations before resuming an older binary, whose
recovery policy can start replacement work.
