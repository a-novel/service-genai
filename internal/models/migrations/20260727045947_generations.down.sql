-- Every generation and every usage record is lost, which is the intended reversal: these tables are
-- the only record of that work and of what it consumed.
DROP TABLE IF EXISTS generation_usage;

DROP TABLE IF EXISTS generations;

DROP TYPE IF EXISTS generation_status;
