-- How many generations one owner has pending or running, read off the partial index on live rows.
SELECT
  count(*) AS count
FROM
  generations
WHERE
  owner_id = ?0
  AND status IN ('pending', 'running');
