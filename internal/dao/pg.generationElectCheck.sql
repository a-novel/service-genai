-- Elects the caller to check a generation nobody checked within the interval. Concurrent readers
-- race on checked_at, so at most one of them reaches the provider per interval.
UPDATE generations
SET
  checked_at = clock_timestamp()
WHERE
  id = ?0
  AND owner_id = ?1
  AND status IN ('pending', 'running')
  AND checked_at <= clock_timestamp() - make_interval(secs => ?2)
RETURNING
  *;
