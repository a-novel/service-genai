-- Usage rows deleted by the up migration because their generation was gone are not restored.
CREATE INDEX generation_usage_owner_purpose_idx ON generation_usage (owner_id, purpose, created_at DESC);

CREATE INDEX generation_usage_owner_idx ON generation_usage (owner_id, created_at DESC);

ALTER TABLE generation_usage
DROP CONSTRAINT IF EXISTS generation_usage_generation_fkey,
DROP COLUMN IF EXISTS reasoning_effort;

ALTER TABLE generations
DROP CONSTRAINT IF EXISTS generations_failure_matches_status,
DROP COLUMN IF EXISTS failure;

ALTER TABLE generations
DROP CONSTRAINT IF EXISTS generations_request_check,
DROP CONSTRAINT IF EXISTS generations_output_check;

ALTER TABLE generations
ALTER COLUMN request TYPE jsonb USING request::jsonb,
ALTER COLUMN output TYPE jsonb USING output::jsonb;

ALTER TABLE generations
ADD CONSTRAINT generations_request_check CHECK (jsonb_typeof(request) = 'object'),
ADD CONSTRAINT generations_output_check CHECK (
  output IS NULL
  OR jsonb_typeof(output) = 'object'
);
