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
  provider_call_id = ?2,
  updated_at = clock_timestamp()
FROM
  held
WHERE
  generations.id = held.id
  AND held.claimed_by = ?1
  AND held.claim_token = ?3
  AND held.status = 'running'
  AND held.lease_expires_at > clock_timestamp()
  AND (
    held.provider_call_id IS NULL
    OR held.provider_call_id = ?2
  )
RETURNING
  generations.*;
