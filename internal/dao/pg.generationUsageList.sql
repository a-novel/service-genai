-- A generation has one row per provider call, bounded by its attempt budget and provider switches.
SELECT
  *
FROM
  generation_usage
WHERE
  generation_id = ?0
ORDER BY
  attempt
LIMIT
  100;
