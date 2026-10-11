-- A no-op DO UPDATE, not DO NOTHING: DO NOTHING returns no row on conflict, losing the replayed
-- generation. This yields the stored row on both paths and takes the row lock, so concurrent
-- submissions of one request resolve to a single winner.
--
-- The conflict target repeats the index predicate: a failed or cancelled generation is outside it, so
-- resending its request inserts a fresh generation.
--
-- The owner's advisory lock holds until the transaction ends, so one owner's admissions run one at a
-- time and none misses another's generation.
--
-- Timestamps are left to the column defaults, since the database is the clock for this table.
WITH
  owner_lock AS MATERIALIZED (
    SELECT
      pg_advisory_xact_lock(hashtextextended (?1::text, 0))
  ),
  generation AS (
    INSERT INTO
      generations (
        id,
        owner_id,
        purpose,
        provider,
        request_key,
        max_attempts,
        traceparent
      )
    SELECT
      ?0::uuid,
      ?1::uuid,
      ?2::text,
      ?3::text,
      ?4::bytea,
      ?5::smallint,
      ?6::text
    FROM
      owner_lock
    ON CONFLICT (request_key)
    WHERE
      status NOT IN ('failed', 'cancelled') DO UPDATE
    SET
      updated_at = generations.updated_at
    RETURNING
      *
  )
SELECT
  *
FROM
  generation;
