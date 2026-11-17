-- A generation runs one start, recorded by its submit, so only a pending generation takes the call. A generation settled while its start was in flight
-- matches nothing, which tells the caller the accepted call is an orphan. The row carries the write's
-- instant as its read, so a check that stops the call measures its ages from it.
UPDATE generations
SET
  provider_call_id = ?1,
  status = 'running',
  updated_at = record.at
FROM
  (
    SELECT
      clock_timestamp() AS at
  ) AS record
WHERE
  generations.id = ?0
  AND generations.status = 'pending'
RETURNING
  generations.*,
  record.at AS read_at;
