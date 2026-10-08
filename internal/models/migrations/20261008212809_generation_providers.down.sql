ALTER TABLE generations
DROP CONSTRAINT IF EXISTS generations_started_with_epoch,
DROP COLUMN IF EXISTS provider_epoch;
