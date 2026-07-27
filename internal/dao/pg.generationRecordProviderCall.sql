-- Guarded on the claim holder: a worker cannot record a call on work it does not hold.
--
-- Recording again overwrites. Whoever started a replacement operation after the first stalled is the
-- authority on which identifier is live.
UPDATE generations
SET
  provider_call_id = ?2,
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND claimed_by = ?1
  AND status = 'running'
RETURNING
  *;
