-- Fenced by the attempt that recorded the start intent. A generation settled while its start was in
-- flight matches nothing, which tells the caller the accepted call is an orphan.
UPDATE generations
SET
  provider_call_id = ?2,
  status = 'running',
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND attempt = ?1
  AND status = 'pending'
  AND start_requested_at IS NOT NULL
RETURNING
  *;
