-- One page of the unsettled generations, in id order from after the given one, so a sweep reads each
-- once however many pages it takes. The read takes no lock and writes nothing: a check writes only to
-- settle. Each row carries the database clock, which its check measures the generation's ages from.
SELECT
  *,
  clock_timestamp() AS read_at
FROM
  generations
WHERE
  status IN ('pending', 'running')
  AND id > ?0
ORDER BY
  id
LIMIT
  ?1;
