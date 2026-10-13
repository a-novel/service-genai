-- A payload can reach 16 MiB, so the purge deletes 1,000 settled generations a call: a backlog drains
-- in transactions short enough for a migration's lock wait. The search path is pinned to the one the
-- migrations ran with, so pg_cron's worker finds the tables whatever its own path.
CREATE OR REPLACE FUNCTION purge_settled_generations () RETURNS bigint LANGUAGE sql
SET
  search_path
FROM
  CURRENT AS $$
  WITH purged AS (
    DELETE FROM generations
    WHERE id = ANY (ARRAY (
      SELECT id FROM generations
      WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP
      ORDER BY expires_at
      LIMIT 1000
    ))
    RETURNING 1
  )
  SELECT count(*) FROM purged;
$$;
