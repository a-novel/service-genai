-- The backlog: how many generations wait for the provider to accept their call, and how long the one
-- waiting longest has waited. A submit requests its generation's start in the insert, so the wait
-- runs from the creation, as it does for a generation an earlier release left without a start.
SELECT
  count(*) AS pending,
  greatest(
    coalesce(
      extract(
        epoch
        FROM
          CURRENT_TIMESTAMP - min(created_at)
      ),
      0
    ),
    0
  ) AS oldest_pending_age_seconds
FROM
  generations
WHERE
  status = 'pending';
