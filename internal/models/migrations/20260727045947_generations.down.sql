-- Dropping each table takes its indexes, constraints and autovacuum settings with it, so an
-- explicit DROP INDEX here would be dead SQL. Explicit drops are only needed when an object
-- outlives the table it belongs to — a view, or an index on a table this migration does not itself
-- drop. Neither applies. The migration roundtrip test proves it: it diffs a full schema census
-- after the down, so an index surviving one would fail.
--
-- Every generation and every cost record is lost, which is the intended reversal: these tables are
-- the only record of that work and of what it cost.
--
-- Reverse creation order: the ledger is dropped before the generations table it describes, and the
-- enum type after the table whose column uses it.
DROP TABLE IF EXISTS generation_ledger;

DROP TABLE IF EXISTS generations;

DROP TYPE IF EXISTS generation_status;
