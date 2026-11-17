-- A generation holds one answer: the limit only bounds the read.
SELECT
  *
FROM
  generation_attempts
WHERE
  generation_id = ?0
ORDER BY
  created_at
LIMIT
  100;
