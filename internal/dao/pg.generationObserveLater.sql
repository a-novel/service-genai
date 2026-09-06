-- Observation retries keep the provider operation attached so another claim continues polling the
-- paid work. Requiring the identifier keeps this transition separate from a fresh inference retry.
UPDATE generations
SET
  status = 'pending',
  claimed_by = NULL,
  lease_expires_at = NULL,
  run_at = clock_timestamp() + make_interval(secs => ?2),
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND claimed_by = ?1
  AND status = 'running'
  AND provider_call_id IS NOT NULL
RETURNING
  *;
