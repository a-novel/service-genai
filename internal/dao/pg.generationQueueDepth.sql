-- The backlog: how many generations wait for the provider to accept their call, due or retrying a
-- refused start, and how long the one waiting longest has waited. A retried start waits from the
-- attempt's first one, so a provider that keeps refusing shows as a growing age, not a burst absorbed.
SELECT
  count(*) AS pending,
  greatest(
    coalesce(
      extract(
        epoch
        FROM
          CURRENT_TIMESTAMP - min(coalesce(first_start_requested_at, run_at))
      ),
      0
    ),
    0
  ) AS oldest_pending_age_seconds
FROM
  generations
WHERE
  status = 'pending';
