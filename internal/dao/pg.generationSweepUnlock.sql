-- Releases the sweep's leader role this session holds. False means the session held no such lock.
SELECT
  pg_advisory_unlock(?0, ?1) AS unlocked;
