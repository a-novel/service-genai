-- Fenced by the start intent the submit recorded: a generation runs one start, so only a pending
-- generation with that start takes the call. A generation settled while its start was in flight
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
  AND generations.start_requested_at IS NOT NULL
RETURNING
  generations.id,
  generations.owner_id,
  generations.purpose,
  generations.provider,
  generations.request_key,
  generations.error,
  generations.failure,
  generations.status,
  generations.start_requested_at,
  generations.provider_call_id,
  generations.cancel_requested_at,
  generations.traceparent,
  generations.outcome_unknown_at,
  generations.streamed,
  generations.created_at,
  generations.updated_at,
  generations.settled_at,
  generations.expires_at,
  record.at AS read_at;
