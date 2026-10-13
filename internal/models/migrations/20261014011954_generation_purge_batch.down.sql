CREATE OR REPLACE FUNCTION purge_settled_generations () RETURNS bigint LANGUAGE sql AS $$
  WITH purged AS (
    DELETE FROM generations
    WHERE id = ANY (ARRAY (
      SELECT id FROM generations
      WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP
      ORDER BY expires_at
      LIMIT 10000
    ))
    RETURNING 1
  )
  SELECT count(*) FROM purged;
$$;
