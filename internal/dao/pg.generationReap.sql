-- Expired claims have no authority during the recovery grace. A known provider operation remains
-- resumable; Start intent without its ID is uncertain and cannot authorize another paid attempt.
WITH
  reapable AS MATERIALIZED (
    SELECT
      generations.*
    FROM
      generations
    WHERE
      status = 'running'
      AND lease_expires_at <= clock_timestamp() - make_interval(secs => ?0)
    ORDER BY
      lease_expires_at,
      id
    LIMIT
      ?2
    FOR UPDATE
      SKIP LOCKED
  ),
  outcomes AS (
    SELECT
      id AS outcome_id,
      CASE
        WHEN provider_call_id IS NOT NULL THEN 'pending'::generation_status
        WHEN start_requested_at IS NOT NULL THEN 'failed'::generation_status
        WHEN cancel_requested_at IS NOT NULL THEN 'cancelled'::generation_status
        WHEN attempt >= max_attempts THEN 'abandoned'::generation_status
        ELSE 'pending'::generation_status
      END AS outcome_status
    FROM
      reapable
  )
UPDATE generations
SET
  status = outcomes.outcome_status,
  error = CASE
    WHEN outcomes.outcome_status = 'failed' THEN 'generation outcome unknown'
    WHEN outcomes.outcome_status = 'cancelled' THEN 'generation cancelled'
    ELSE error
  END,
  claimed_by = NULL,
  claim_token = NULL,
  lease_expires_at = NULL,
  run_at = clock_timestamp(),
  settled_at = CASE
    WHEN outcomes.outcome_status <> 'pending' THEN clock_timestamp()
  END,
  expires_at = CASE
    WHEN outcomes.outcome_status <> 'pending' THEN clock_timestamp() + make_interval(secs => ?1)
  END,
  updated_at = clock_timestamp()
FROM
  outcomes
WHERE
  generations.id = outcomes.outcome_id
  AND generations.status = 'running'
  AND generations.lease_expires_at <= clock_timestamp() - make_interval(secs => ?0)
RETURNING
  generations.*;
