-- FOR UPDATE SKIP LOCKED so concurrent claims take disjoint batches instead of queueing behind each
-- other. The lease is added to the database clock, so a worker's own clock never enters it.
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
      run_at
    LIMIT
      ?1
    FOR UPDATE
      SKIP LOCKED
  )
UPDATE generations
SET
  status = 'running',
  attempt = generations.attempt + 1,
  claimed_by = ?0,
  lease_expires_at = clock_timestamp() + make_interval(secs => ?2),
  updated_at = clock_timestamp()
FROM
  claimable
WHERE
  generations.id = claimable.id
RETURNING
  generations.*;
