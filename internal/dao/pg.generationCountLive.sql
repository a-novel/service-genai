-- How many generations one owner has pending or running, for one purpose or every one, read off the
-- partial index on live rows.
SELECT
  count(*) AS count
FROM
  generations
WHERE
  owner_id = ?0
  AND (
    ?1::text IS NULL
    OR purpose = ?1
  )
  AND status IN ('pending', 'running');
