-- Fenced by the attempt that recorded the start intent. A generation settled while its start was in
-- flight matches nothing, which tells the caller the accepted call is an orphan. The row carries the
-- write's instant as its read, so a check that stops the call measures its ages from it.
UPDATE generations
SET
  provider_call_id = ?2,
  status = 'running',
  updated_at = record.at
FROM
  (
    SELECT
      clock_timestamp() AS at
  ) AS record
WHERE
  generations.id = ?0
  AND generations.attempt = ?1
  AND generations.status = 'pending'
  AND generations.start_requested_at IS NOT NULL
RETURNING
  generations.*,
  record.at AS read_at;
