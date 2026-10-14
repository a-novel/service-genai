-- The purge deletes 1,000 settled generations a call. Called every minute, it kept up with about 16
-- settling a second across the fleet; called every 10 seconds, it keeps up with 100, in
-- transactions as short as before.
--
-- pg_cron records every run of every job and deletes none, so a second job deletes the service's
-- runs older than a week, once a day. Scheduling a name again replaces its job.
DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_extension WHERE extname = 'pg_cron') THEN
    PERFORM cron.schedule(
      'service-genai-purge-settled',
      '10 seconds',
      format('SELECT %I.purge_settled_generations()', current_schema())
    );
    PERFORM cron.schedule(
      'service-genai-purge-cron-runs',
      '0 4 * * *',
      $job$
        DELETE FROM cron.job_run_details
        WHERE end_time < now() - interval '7 days'
          AND jobid IN (SELECT jobid FROM cron.job WHERE jobname LIKE 'service-genai-%')
      $job$
    );
  END IF;
END
$$;
