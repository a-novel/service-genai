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
  status = 'pending',
  claimed_by = NULL,
  claim_token = NULL,
  lease_expires_at = NULL,
  provider_call_id = NULL,
  start_requested_at = NULL,
  run_at = clock_timestamp(),
  updated_at = clock_timestamp()
FROM
  held
WHERE
  id = held.held_id
  AND claimed_by = ?1
  AND claim_token = ?3
  AND status = 'running'
  AND lease_expires_at > clock_timestamp()
  AND provider_call_id IS NOT DISTINCT FROM ?2
RETURNING
  generations.*;
