-- The owner is part of the predicate, not checked after the fact. A caller that omits it fails to
-- scan rather than returning another owner's row, and a generation belonging to someone else is
-- indistinguishable from one that does not exist — so an identifier cannot be probed for existence.
SELECT
  *
FROM
  generations
WHERE
  id = ?0
  AND owner_id = ?1;
