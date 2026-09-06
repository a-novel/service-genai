-- Lock first so expiry is evaluated after any wait for a concurrent transaction.
WITH
  held AS MATERIALIZED (
    SELECT
      id AS held_id
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
  id = held.held_id
  AND claimed_by = ?1
  AND claim_token = ?6
  AND status = 'running'
  AND lease_expires_at > clock_timestamp()
RETURNING
  generations.*;
