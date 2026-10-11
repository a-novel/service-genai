-- The generation running a provider call, read off the partial index on running calls. A call id is
-- recorded on one generation at most.
SELECT
  *
FROM
  generations
WHERE
  provider = ?0
  AND provider_call_id = ?1
  AND status = 'running'
LIMIT
  1;
