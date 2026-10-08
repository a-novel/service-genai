-- Gives the attempt back: its call was never accepted, so it cost nothing and another may start
-- after the delay.
UPDATE generations
SET
  attempt = attempt - 1,
  start_requested_at = NULL,
  run_at = clock_timestamp() + make_interval(secs => ?2),
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND attempt = ?1
  AND status = 'pending'
  AND start_requested_at IS NOT NULL
RETURNING
  *;
