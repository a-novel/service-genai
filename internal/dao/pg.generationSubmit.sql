-- A no-op DO UPDATE, not DO NOTHING: DO NOTHING returns no row on conflict, losing the replayed
-- generation. This yields the stored row on both paths and takes the row lock, so concurrent
-- submissions of one key resolve to a single winner.
--
-- Timestamps are left to the column defaults, since the database is the clock for this table.
INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    profile,
    idempotency_key,
    request_fingerprint,
    request,
    max_attempts
  )
VALUES
  (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7)
ON CONFLICT (owner_id, purpose, idempotency_key) DO UPDATE
SET
  updated_at = generations.updated_at
RETURNING
  *;
