-- The conflict target is the unique index on (owner_id, purpose, idempotency_key).
--
-- DO UPDATE with a no-op assignment rather than DO NOTHING: DO NOTHING returns no row on conflict,
-- which loses the replayed generation exactly when the caller needs it most. The no-op assignment
-- makes RETURNING yield the stored row on both paths, and it takes the row lock, so two concurrent
-- submissions of the same key resolve to one winner instead of one of them scanning nothing.
--
-- Timestamps and the fallback identifier are left to the column defaults. The database is the
-- single clock for this table, so an application-supplied time could disagree with the lease
-- arithmetic computed beside it.
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
