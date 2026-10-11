-- When a generation settled with its start's outcome unknown. The provider may still run a call that
-- nothing tracks or meters, so admission keeps counting the generation for a while after.
ALTER TABLE generations
ADD COLUMN outcome_unknown_at timestamp with time zone,
ADD CONSTRAINT generations_outcome_unknown_failed CHECK (
  outcome_unknown_at IS NULL
  OR status = 'failed'
);

-- Partial: only such generations, so admission's count reads them off the owner's few rows.
CREATE INDEX generations_outcome_unknown_owner_idx ON generations (owner_id, outcome_unknown_at)
WHERE
  outcome_unknown_at IS NOT NULL;
