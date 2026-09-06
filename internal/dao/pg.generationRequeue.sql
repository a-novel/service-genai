-- A definitive provider outcome authorizes another inference attempt only when the worker names
-- the exact operation it observed. A NULL expectation covers provider rejection before creation.
UPDATE generations
SET
  status = 'pending',
  claimed_by = NULL,
  lease_expires_at = NULL,
  provider_call_id = NULL,
  run_at = clock_timestamp(),
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND claimed_by = ?1
  AND status = 'running'
  AND provider_call_id IS NOT DISTINCT FROM ?2
RETURNING
  *;
