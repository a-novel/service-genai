-- pg_cron runs the retention purge, which the migrations schedule once the extension exists here.
CREATE EXTENSION IF NOT EXISTS pg_cron;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
