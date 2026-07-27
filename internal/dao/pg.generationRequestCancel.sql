-- Marks the request. The worker observes it at its next poll and stops the provider call; nothing
-- here settles the generation, because the tokens spent before the stop still have to be recorded.
--
-- Owner-scoped, and only while the generation can still be stopped: a terminal one is already paid
-- for and a cancel would be a no-op the caller should be told about.
UPDATE generations
SET
  cancel_requested_at = coalesce(cancel_requested_at, clock_timestamp()),
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND owner_id = ?1
  AND status IN ('pending', 'running')
RETURNING
  *;
