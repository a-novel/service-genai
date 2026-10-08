CREATE EXTENSION IF NOT EXISTS pg_cron;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Deletes settled generations once their window has passed, and with them their usage rows: callers
-- keep the long-term usage record.
--
-- Scheduled here rather than in a migration: RunDBTest clones a database with no cron schema, so a
-- migration calling cron.schedule would fail every data-access test. The cost is that a
-- bring-your-own-Postgres deployment must schedule it itself — see CONTRIBUTING.md.
--
-- The job name is service-scoped because pg_cron names are global to the instance.
SELECT
  cron.schedule (
    'service-genai-purge-settled',
    '*/10 * * * *',
    $$DELETE FROM generations WHERE expires_at IS NOT NULL AND expires_at < clock_timestamp()$$
  );
