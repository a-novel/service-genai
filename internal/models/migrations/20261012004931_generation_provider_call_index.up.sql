-- A provider's notification names one of its calls: the generation running it is found by provider
-- and call id. Only a running generation has anything left to check, so the index holds the calls in
-- flight and stays as small as the service's concurrency.
CREATE INDEX generations_provider_call_idx ON generations (provider, provider_call_id)
WHERE
  status = 'running';
