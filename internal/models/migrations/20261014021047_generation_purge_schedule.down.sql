DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_extension WHERE extname = 'pg_cron') THEN
    PERFORM cron.unschedule(jobid) FROM cron.job WHERE jobname = 'service-genai-purge-cron-runs';
    PERFORM cron.schedule(
      'service-genai-purge-settled',
      '* * * * *',
      format('SELECT %I.purge_settled_generations()', current_schema())
    );
  END IF;
END
$$;
