-- The retention purge lives in the schema, so a release changes it like any other object and tests
-- run it. Scheduled by the database image's init SQL instead, it stayed as the volume's first start
-- wrote it.
--
-- It deletes settled generations once their window has passed, and their payloads and attempts with
-- them; the Usage Records stay for good. Each call deletes at most one batch, so a backlog left by
-- missed runs drains in short transactions. The cutoff is stable, so the retention index bounds the
-- scan, and the batch is an array so each row is deleted through its key. It returns how many it
-- deleted.
CREATE FUNCTION purge_settled_generations () RETURNS bigint LANGUAGE sql AS $$
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

-- pg_cron calls it every minute wherever the extension is installed, as the database image does.
-- Databases without it, such as the test clones, skip the block. Job names are global to the
-- instance, hence the service's prefix, and scheduling a name again replaces its job.
DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_extension WHERE extname = 'pg_cron') THEN
    PERFORM cron.schedule(
      'service-genai-purge-settled',
      '* * * * *',
      format('SELECT %I.purge_settled_generations()', current_schema())
    );
  END IF;
END
$$;
