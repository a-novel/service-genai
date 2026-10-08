-- Fenced by the attempt and provider call the caller observed, so a check acting on an outdated read
-- cannot settle an attempt it never saw.
UPDATE generations
SET
  status = ?3,
  output = ?4,
  error = ?5,
  settled_at = clock_timestamp(),
  expires_at = clock_timestamp() + make_interval(secs => ?6),
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND attempt = ?1
  AND provider_call_id IS NOT DISTINCT FROM ?2
  AND status IN ('pending', 'running')
RETURNING
  *;
