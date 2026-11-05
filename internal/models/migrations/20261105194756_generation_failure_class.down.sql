-- The failure classes are lost: the error message each failed generation keeps says why it failed.
ALTER TABLE generations
DROP CONSTRAINT IF EXISTS generations_failure_failed,
DROP COLUMN IF EXISTS failure;

DROP TYPE IF EXISTS generation_failure;
