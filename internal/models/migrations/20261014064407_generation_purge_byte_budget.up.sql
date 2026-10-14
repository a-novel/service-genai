-- A purge holds its locks for as long as it takes to delete what its rows store, and one generation
-- can store tens of MiB: 1,000 large ones kept a call deleting for seconds, past the three seconds a
-- migration waits for a lock. A call now stops once the generations it takes store max_bytes, 64 MiB
-- by default, and always takes one, so a backlog of large generations still drains.
DROP FUNCTION purge_settled_generations ();

CREATE FUNCTION purge_settled_generations (max_bytes bigint DEFAULT 67108864) RETURNS bigint LANGUAGE sql
SET
  search_path
FROM
  CURRENT AS $$
  WITH expired AS (
    SELECT
      id,
      expires_at,
      coalesce((SELECT pg_column_size(payload) FROM generation_payloads WHERE generation_id = generations.id), 0)
        + coalesce((SELECT sum(pg_column_size(response)) FROM generation_attempts WHERE generation_id = generations.id), 0)
        AS stored
    FROM generations
    WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP
    ORDER BY expires_at
    LIMIT 1000
  ),
  purged AS (
    DELETE FROM generations
    WHERE id = ANY (ARRAY (
      SELECT id FROM (
        SELECT id, sum(stored) OVER (ORDER BY expires_at, id) - stored AS stored_before FROM expired
      ) AS batch
      WHERE stored_before < max_bytes
    ))
    RETURNING 1
  )
  SELECT count(*) FROM purged;
$$;
