-- A no-op DO UPDATE, not DO NOTHING: DO NOTHING returns no row on conflict, losing the replayed
-- generation. This yields the stored row on both paths and takes the row lock, so concurrent
-- submissions of one request resolve to a single winner.
--
-- The conflict target repeats the index predicate: a failed or cancelled generation is outside it, so
-- resending its request inserts a fresh generation.
--
-- Timestamps are left to the column defaults, since the database is the clock for this table.
INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    request_key,
    request,
    max_attempts
  )
VALUES
  (?0, ?1, ?2, ?3, ?4, ?5)
ON CONFLICT (request_key)
WHERE
  status NOT IN ('failed', 'cancelled') DO UPDATE
SET
  updated_at = generations.updated_at
RETURNING
  *;
