-- FOR UPDATE SKIP LOCKED so concurrent claims take disjoint batches instead of queueing behind each
-- other. The lease is added to the database clock, so a worker's own clock never enters it.
-- Resuming an existing provider operation keeps its inference attempt and usage identity.
WITH
  claimable AS (
    SELECT
      id
    FROM
      generations
    WHERE
      status = 'pending'
      AND run_at <= clock_timestamp()
    ORDER BY
      run_at,
      id
    LIMIT
      ?1
    FOR UPDATE
      SKIP LOCKED
  )
UPDATE generations
SET
  status = 'running',
  claim_token = uuidv7(),
  attempt = generations.attempt + CASE
    WHEN generations.provider_call_id IS NULL
    AND generations.start_requested_at IS NULL THEN 1
    ELSE 0
  END,
  claimed_by = ?0,
  lease_expires_at = clock_timestamp() + make_interval(secs => ?2),
  updated_at = clock_timestamp()
FROM
  claimable
WHERE
  generations.id = claimable.id
RETURNING
  generations.*;
