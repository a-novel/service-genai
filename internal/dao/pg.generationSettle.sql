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
  status = ?2,
  output = ?3,
  error = ?4,
  claim_token = NULL,
  lease_expires_at = NULL,
  settled_at = clock_timestamp(),
  expires_at = clock_timestamp() + make_interval(secs => ?5),
  updated_at = clock_timestamp()
FROM
  held
WHERE
  generations.id = held.id
  AND held.claimed_by = ?1
  AND held.claim_token = ?6
  AND held.status = 'running'
  AND held.lease_expires_at > clock_timestamp()
RETURNING
  generations.*;
