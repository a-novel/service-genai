-- By this release, the sweep has settled the generation the first fixture left without a start
-- intent, and the purge has deleted it, as it did every such row in production.
DELETE FROM generations
WHERE
  start_requested_at IS NULL;
