-- A retryable failure with attempts left. The worker that reported it declared its provider
-- operation dead, so provider_call_id is cleared and the next run starts a fresh one — the opposite
-- of what the reaper does.
UPDATE generations
SET
  status = 'pending',
  claimed_by = NULL,
  lease_expires_at = NULL,
  provider_call_id = NULL,
  run_at = clock_timestamp(),
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND claimed_by = ?1
  AND status = 'running'
RETURNING
  *;
