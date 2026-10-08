-- Fenced by the attempt and provider call the caller observed, so a check acting on an outdated read
-- cannot settle an attempt it never saw.
--
-- One instant serves every timestamp: two clock_timestamp() calls in one statement can differ, and
-- the retention between settled_at and expires_at must be exact.
UPDATE generations
SET
  status = ?3,
  output = ?4,
  error = ?5,
  failure = ?7,
  settled_at = settle.at,
  expires_at = settle.at + make_interval(secs => ?6),
  updated_at = settle.at
FROM
  (
    SELECT
      clock_timestamp() AS at
  ) AS settle
WHERE
  generations.id = ?0
  AND generations.attempt = ?1
  AND generations.provider_call_id IS NOT DISTINCT FROM ?2
  AND generations.status IN ('pending', 'running')
RETURNING
  generations.*;
