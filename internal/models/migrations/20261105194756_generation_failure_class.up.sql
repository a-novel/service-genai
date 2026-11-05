-- Why a generation failed, as a value its caller reads to decide whether to send the request again:
-- the provider refused it, failed the call, never received it, or may still be running it.
CREATE TYPE generation_failure AS ENUM(
  'rejected',
  'provider_failed',
  'not_started',
  'outcome_unknown'
);

-- The column is nullable, so adding it changes only the catalog. The check scans the table under its
-- lock, which stays short: every row holds no class yet, and the purge keeps the table to a few hours
-- of generations.
ALTER TABLE generations
ADD COLUMN failure generation_failure,
ADD CONSTRAINT generations_failure_failed CHECK (
  failure IS NULL
  OR status = 'failed'
);
