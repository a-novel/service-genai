-- A generation has one row per answered provider call, bounded by its attempt budget.
SELECT
  *
FROM
  generation_attempts
WHERE
  generation_id = ?0
ORDER BY
  attempt
LIMIT
  100;
