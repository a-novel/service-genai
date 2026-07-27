-- Every generation and every cost record is lost, which is the intended reversal: these tables are
-- the only record of that work and of what it cost.
DROP TABLE IF EXISTS generation_ledger;

DROP TABLE IF EXISTS generations;

DROP TYPE IF EXISTS generation_status;
