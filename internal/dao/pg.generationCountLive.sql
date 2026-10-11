-- How many generations one owner has in flight, for one purpose or every one: pending, running, or
-- failed within the window with a start the provider may have accepted. Each kind is read off its
-- own partial index.
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
  AND (
    status IN ('pending', 'running')
    OR outcome_unknown_at > CURRENT_TIMESTAMP - make_interval(secs => ?2)
  );
