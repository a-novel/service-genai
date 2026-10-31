-- pg_cron runs the retention purge, which the migrations job schedules once the extension exists here.
CREATE EXTENSION IF NOT EXISTS pg_cron;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
