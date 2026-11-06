-- The sweep pages through the unsettled generations in id order. Partial, so the index holds only
-- the generations in flight, however many settled ones await the purge.
--
-- The index builds concurrently, so writes to generations go on during the build. A concurrent build
-- refuses a transaction, so each statement runs on its own. A build that gives up on a lock leaves an
-- invalid index of its name behind, and the migrations job runs the file again: its first statement
-- drops that leftover.
DROP INDEX CONCURRENTLY IF EXISTS generations_sweep_idx;

--bun:split
CREATE INDEX CONCURRENTLY generations_sweep_idx ON generations (id)
WHERE
  status IN ('pending', 'running');
