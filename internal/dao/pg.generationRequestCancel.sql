-- A generation with no start intent has no provider call to stop, so it is settled here. One whose
-- call may already be accepted is only marked: the next check stops the call and settles it with
-- whatever it consumed.
--
-- Owner-scoped, and only while the generation can still be stopped: a terminal one is already paid
-- for and a cancel would be a no-op the caller should be told about.
UPDATE generations
SET
  cancel_requested_at = coalesce(cancel_requested_at, clock_timestamp()),
  status = CASE
    WHEN start_requested_at IS NULL THEN 'cancelled'::generation_status
    ELSE status
  END,
  error = CASE
    WHEN start_requested_at IS NULL THEN ?2
    ELSE error
  END,
  settled_at = CASE
    WHEN start_requested_at IS NULL THEN clock_timestamp()
  END,
  expires_at = CASE
    WHEN start_requested_at IS NULL THEN clock_timestamp() + make_interval(secs => ?3)
  END,
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND owner_id = ?1
  AND status IN ('pending', 'running')
RETURNING
  *;
