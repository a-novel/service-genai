DROP INDEX IF EXISTS generations_outcome_unknown_owner_idx;

ALTER TABLE generations
DROP CONSTRAINT IF EXISTS generations_outcome_unknown_failed,
DROP COLUMN IF EXISTS outcome_unknown_at;
