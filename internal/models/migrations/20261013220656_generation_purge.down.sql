-- Unscheduled first, before the function its job calls is dropped.
DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_extension WHERE extname = 'pg_cron') THEN
    PERFORM cron.unschedule(jobid) FROM cron.job WHERE jobname = 'service-genai-purge-settled';
  END IF;
END
$$;

DROP FUNCTION IF EXISTS purge_settled_generations ();
