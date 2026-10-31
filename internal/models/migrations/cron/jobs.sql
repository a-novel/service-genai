-- The service's pg_cron jobs. The migrations command runs this file after the migrations, on every
-- run, so a change to a job goes here. A database import keeps the migration history but leaves the
-- jobs out: pg_cron's row-level security hides them from a dump by any role but a superuser.
--
-- Scheduling a name again replaces the role's own job of that name. A job another role owns keeps
-- its owner and takes the definition below, as on a database host, whose boot hands the jobs to the
-- service's own role. Only a superuser sees such a job and may alter it, so the alter runs only when
-- one exists. Databases without pg_cron, such as the test clones, skip the block.
DO $$
DECLARE
  definition record;
BEGIN
  IF NOT EXISTS (SELECT FROM pg_extension WHERE extname = 'pg_cron') THEN
    RETURN;
  END IF;

  FOR definition IN
    SELECT * FROM (
      VALUES
        -- Every 10 seconds, the purge keeps up with 100 generations settling a second.
        (
          'service-genai-purge-settled',
          '10 seconds',
          format('SELECT %I.purge_settled_generations()', current_schema())
        ),
        -- pg_cron records every run of every job and deletes none. The service's runs older than a
        -- week go once a day.
        (
          'service-genai-purge-cron-runs',
          '0 4 * * *',
          $job$
            DELETE FROM cron.job_run_details
            WHERE end_time < CURRENT_TIMESTAMP - interval '7 days'
              AND jobid IN (SELECT jobid FROM cron.job WHERE jobname LIKE 'service-genai-%')
          $job$
        )
    ) AS jobs (name, schedule, command)
  LOOP
    IF EXISTS (SELECT FROM cron.job WHERE jobname = definition.name AND username <> current_user) THEN
      PERFORM cron.alter_job(jobid, schedule => definition.schedule, command => definition.command)
      FROM cron.job
      WHERE jobname = definition.name AND username <> current_user;
    ELSE
      PERFORM cron.schedule(definition.name, definition.schedule, definition.command);
    END IF;
  END LOOP;
END
$$;
