-- The byte budget counts what a generation's attempts store, since the service stores no payload.
-- PostgreSQL records no dependency on the tables a function body names, so the purge stops naming
-- generation_payloads before a later migration drops the table. A purge still naming it fails on
-- every pg_cron run.
CREATE OR REPLACE FUNCTION purge_settled_generations (max_bytes bigint DEFAULT 67108864) RETURNS bigint LANGUAGE sql
SET
  search_path
FROM
  CURRENT AS $$
  WITH expired AS (
    SELECT
      id,
      expires_at,
      coalesce((SELECT sum(pg_column_size(response)) FROM generation_attempts WHERE generation_id = generations.id), 0)
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
