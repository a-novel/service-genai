-- Takes the next attempt and records that its call may be sent. The conditions read the row's own
-- state, so of two concurrent checks only the first to commit matches. The attempt runs on the
-- caller's provider epoch, which a replica on an older one must not take back.
UPDATE generations
SET
  attempt = attempt + 1,
  start_requested_at = clock_timestamp(),
  provider_epoch = ?1,
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND status = 'pending'
  AND start_requested_at IS NULL
  AND cancel_requested_at IS NULL
  AND run_at <= clock_timestamp()
  AND coalesce(provider_epoch, 0) <= ?1
RETURNING
  *;
