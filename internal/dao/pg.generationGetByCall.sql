-- The generation running a provider call, read off the partial index on running calls. A call id is
-- recorded on one generation at most. The read carries the database clock, which a check measures the
-- generation's ages from.
SELECT
  *,
  clock_timestamp() AS read_at
FROM
  generations
WHERE
  provider = ?0
  AND provider_call_id = ?1
  AND status = 'running'
LIMIT
  1;
