-- The backlog: how many generations wait to start, and how long the oldest has waited.
SELECT
  count(*) AS pending,
  coalesce(
    extract(
      epoch
      FROM
        clock_timestamp() - min(run_at)
    ),
    0
  ) AS oldest_pending_age_seconds
FROM
  generations
WHERE
  status = 'pending'
  AND start_requested_at IS NULL
  AND run_at <= clock_timestamp();
