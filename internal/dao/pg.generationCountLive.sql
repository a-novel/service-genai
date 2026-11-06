-- How many generations one owner has in flight for one purpose: pending, running, or failed within
-- the window with a call the provider may still run. Each kind is read off its own partial index.
SELECT
  count(*) AS count
FROM
  generations
WHERE
  owner_id = ?0
  AND purpose = ?1
  AND (
    status IN ('pending', 'running')
    OR outcome_unknown_at > CURRENT_TIMESTAMP - make_interval(secs => ?2)
  );
