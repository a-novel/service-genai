-- Fenced by the start and the provider call the caller observed, so a check acting on an outdated read
-- cannot settle a call it never saw. A generation runs one start, so the two name its run.
--
-- One instant serves every timestamp: two clock_timestamp() calls in one statement can differ, and
-- the retention between settled_at and expires_at must be exact.
UPDATE generations
SET
  status = ?2,
  error = ?3,
  failure = ?7,
  settled_at = settle.at,
  expires_at = settle.at + make_interval(secs => ?4),
  outcome_unknown_at = CASE
    WHEN ?6 THEN settle.at
  END,
  updated_at = settle.at
FROM
  (
    SELECT
      clock_timestamp() AS at
  ) AS settle
WHERE
  generations.id = ?0
  AND generations.provider_call_id IS NOT DISTINCT FROM ?1
  AND generations.start_requested_at IS NOT DISTINCT FROM ?5
  AND generations.status IN ('pending', 'running')
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
  generations.expires_at;
