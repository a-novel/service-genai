-- Takes the generations nobody checked within the interval, stalest first, and marks them checked.
-- SKIP LOCKED gives concurrent sweeps disjoint batches instead of queueing them behind each other.
WITH
  due AS (
    SELECT
      id
    FROM
      generations
    WHERE
      status IN ('pending', 'running')
      AND checked_at <= clock_timestamp() - make_interval(secs => ?0)
    ORDER BY
      checked_at,
      id
    LIMIT
      ?1
    FOR UPDATE
      SKIP LOCKED
  )
UPDATE generations
SET
  checked_at = clock_timestamp()
FROM
  due
WHERE
  generations.id = due.id
RETURNING
  generations.*;
