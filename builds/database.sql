CREATE EXTENSION IF NOT EXISTS pg_cron;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Deletes settled generations once their window has passed, and with them their payloads and
-- attempts: callers keep what they need of the provider's responses. The Usage Records are kept for
-- good.
--
-- Scheduled here rather than in a migration: RunDBTest clones a database with no cron schema, so a
-- migration calling cron.schedule would fail every data-access test. The cost is that a
-- bring-your-own-Postgres deployment must schedule it itself; see CONTRIBUTING.md.
--
-- The job name is service-scoped because pg_cron names are global to the instance. Each run deletes
-- at most one batch, so a backlog left by missed runs drains in short transactions. The cutoff is
-- stable, so the retention index bounds the scan, and the batch is an array so each row is deleted
-- through its key.
SELECT
  cron.schedule (
    'service-genai-purge-settled',
    '* * * * *',
    $$DELETE FROM generations WHERE id = ANY(ARRAY(SELECT id FROM generations WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP ORDER BY expires_at LIMIT 10000))$$
  );
