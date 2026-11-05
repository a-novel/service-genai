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
-- A created generation carries its start intent: the submit sends its call once the transaction
-- commits, so from then on the provider may hold paid work for its one attempt. The intent reads the
-- clock once the owner's lock is held, and both starts take that instant. The other timestamps are
-- left to the column defaults, since the database is the clock for this table.
WITH
  owner_lock AS MATERIALIZED (
    SELECT
      pg_advisory_xact_lock(hashtextextended (?1::text, 0))
  ),
  intent AS MATERIALIZED (
    SELECT
      clock_timestamp() AS at
    FROM
      owner_lock
  ),
  generation AS (
    INSERT INTO
      generations (
        id,
        owner_id,
        purpose,
        provider,
        request_key,
        traceparent,
        streamed,
        attempt,
        start_requested_at,
        first_start_requested_at
      )
    SELECT
      ?0::uuid,
      ?1::uuid,
      ?2::text,
      ?3::text,
      ?4::bytea,
      ?5::text,
      ?6::boolean,
      1,
      intent.at,
      intent.at
    FROM
      intent
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
