-- The owner is in the predicate, not checked after the fact, so another owner's generation is
-- indistinguishable from one that does not exist and an identifier cannot be probed. The read carries
-- the database clock, which a check measures the generation's ages from.
SELECT
  *,
  clock_timestamp() AS read_at
FROM
  generations
WHERE
  id = ?0
  AND owner_id = ?1;
