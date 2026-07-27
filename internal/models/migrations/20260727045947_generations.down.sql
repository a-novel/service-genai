-- Dropping each table takes its indexes, constraints and autovacuum settings with it, so there is
-- nothing to unwind first. Every generation and every cost record is lost, which is the intended
-- reversal: these tables are the only record of that work and of what it cost.
--
-- Reverse creation order: the ledger is dropped before the generations table it describes, and the
-- enum type after the table whose column uses it.
DROP TABLE IF EXISTS generation_ledger;

DROP TABLE IF EXISTS generations;

DROP TYPE IF EXISTS generation_status;
