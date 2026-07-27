-- Recovers generations whose lease lapsed. provider_call_id is PRESERVED: the worker died, the
-- provider's operation did not, so the next claim re-attaches instead of paying again.
--
-- The grace period is a head start for a late settle over the sweep. It is applied in the predicate
-- rather than at boot, because other replicas would sweep straight through a boot-only grace.
--
-- Bounded: a sweep that found every stranded claim at once would materialise the whole set back to
-- the caller. The caller repeats until a sweep comes back short.
WITH
  reapable AS (
    SELECT
      id
    FROM
      generations
    WHERE
      status = 'running'
      AND lease_expires_at < clock_timestamp() - make_interval(secs => ?0)
    ORDER BY
      lease_expires_at
    LIMIT
      ?2
    FOR UPDATE
      SKIP LOCKED
  )
UPDATE generations
SET
  status = CASE
    WHEN generations.attempt >= generations.max_attempts THEN 'abandoned'::generation_status
    ELSE 'pending'::generation_status
  END,
  claimed_by = NULL,
  lease_expires_at = NULL,
  run_at = clock_timestamp(),
  settled_at = CASE
    WHEN generations.attempt >= generations.max_attempts THEN clock_timestamp()
  END,
  expires_at = CASE
    WHEN generations.attempt >= generations.max_attempts THEN clock_timestamp() + make_interval(secs => ?1)
  END,
  updated_at = clock_timestamp()
FROM
  reapable
WHERE
  generations.id = reapable.id
RETURNING
  generations.*;
