-- Two numbers, not one: a count alone cannot tell a queue absorbing a burst from a stalled one, and
-- the oldest pending age is what separates them.
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
  AND run_at <= clock_timestamp();
