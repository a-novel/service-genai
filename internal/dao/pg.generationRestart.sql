-- Discards an attempt started on an older provider configuration and returns the generation to the
-- queue under the caller's. Fenced like a settle, so a check that read a stale attempt matches
-- nothing. The discarded attempt was the provider's fault, not the caller's, so it is not counted
-- against the retry budget.
UPDATE generations
SET
  status = 'pending',
  provider_call_id = NULL,
  start_requested_at = NULL,
  provider_epoch = ?3,
  max_attempts = max_attempts + 1,
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND attempt = ?1
  AND provider_call_id IS NOT DISTINCT FROM ?2
  AND provider_epoch < ?3
  AND status IN ('pending', 'running')
  AND start_requested_at IS NOT NULL
RETURNING
  *;
