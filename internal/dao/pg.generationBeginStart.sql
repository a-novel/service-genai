-- Read authority from the locked CTE row: filtering the target table can evaluate expiry
-- before the lock wait, even when the CTE is materialized.
WITH
  held AS MATERIALIZED (
    SELECT
      generations.*
    FROM
      generations
    WHERE
      id = ?0
    FOR UPDATE
  )
UPDATE generations
SET
  start_requested_at = CASE
    WHEN held.cancel_requested_at IS NULL THEN clock_timestamp()
  END,
  updated_at = clock_timestamp()
FROM
  held
WHERE
  generations.id = held.id
  AND held.claimed_by = ?1
  AND held.claim_token = ?2
  AND held.status = 'running'
  AND held.lease_expires_at > clock_timestamp()
  AND held.provider_call_id IS NULL
  AND held.start_requested_at IS NULL
RETURNING
  generations.*;
