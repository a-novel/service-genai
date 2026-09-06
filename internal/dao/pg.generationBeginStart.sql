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
  start_requested_at = CASE
    WHEN cancel_requested_at IS NULL THEN clock_timestamp()
  END,
  updated_at = clock_timestamp()
FROM
  held
WHERE
  id = held.held_id
  AND claimed_by = ?1
  AND claim_token = ?2
  AND status = 'running'
  AND lease_expires_at > clock_timestamp()
  AND provider_call_id IS NULL
  AND start_requested_at IS NULL
RETURNING
  generations.*;
