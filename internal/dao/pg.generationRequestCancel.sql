-- Only marks the generation: the next check stops its call and settles it with whatever it consumed.
--
-- Owner-scoped, and only while the generation can still be stopped: a settled one is already paid
-- for and matches nothing, so the caller reads it as it stands. The row carries the cancel's instant
-- as its read, for the check that stops the call.
UPDATE generations
SET
  cancel_requested_at = coalesce(cancel_requested_at, cancel.at),
  updated_at = cancel.at
FROM
  (
    SELECT
      clock_timestamp() AS at
  ) AS cancel
WHERE
  generations.id = ?0
  AND generations.owner_id = ?1
  AND generations.status IN ('pending', 'running')
RETURNING
  generations.*,
  cancel.at AS read_at;
