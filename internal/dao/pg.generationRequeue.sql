-- Returns a failed attempt to the queue for a fresh call after the delay. Its provider call is
-- finished, so clearing it loses nothing.
UPDATE generations
SET
  status = 'pending',
  provider_call_id = NULL,
  start_requested_at = NULL,
  run_at = clock_timestamp() + make_interval(secs => ?3),
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND attempt = ?1
  AND provider_call_id = ?2
  AND status = 'running'
RETURNING
  *;
