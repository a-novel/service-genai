-- json keeps a document's text as sent; jsonb reorders object keys. The output schema's property
-- order steers what the model writes first, so it must reach the provider unchanged.
ALTER TABLE generations
DROP CONSTRAINT generations_request_check,
DROP CONSTRAINT generations_output_check;

ALTER TABLE generations
ALTER COLUMN request TYPE json USING request::json,
ALTER COLUMN output TYPE json USING output::json;

ALTER TABLE generations
ADD CONSTRAINT generations_request_check CHECK (json_typeof(request) = 'object'),
ADD CONSTRAINT generations_output_check CHECK (
  output IS NULL
  OR json_typeof(output) = 'object'
);

-- A failed generation says what kind of failure ended it, so the caller can decide what to do.
ALTER TABLE generations
ADD COLUMN failure text CHECK (
  failure IN (
    'refused',
    'incomplete',
    'invalid_request',
    'failed'
  )
);

UPDATE generations
SET
  failure = 'failed'
WHERE
  status = 'failed';

ALTER TABLE generations
ADD CONSTRAINT generations_failure_matches_status CHECK ((status = 'failed') = (failure IS NOT NULL));

-- Callers keep long-term usage, so this record only lives as long as the generation it describes.
-- Usage rows whose generation was already purged go now, which the down migration cannot restore.
DELETE FROM generation_usage
WHERE
  NOT EXISTS (
    SELECT
      1
    FROM
      generations
    WHERE
      generations.id = generation_usage.generation_id
  );

ALTER TABLE generation_usage
/* The effort that actually ran, read off the provider's response. Null for a model without one. */
ADD COLUMN reasoning_effort text CHECK (
  reasoning_effort IS NULL
  OR reasoning_effort <> ''
),
ADD CONSTRAINT generation_usage_generation_fkey FOREIGN KEY (generation_id) REFERENCES generations (id) ON DELETE CASCADE;

-- They served the usage query, which callers' own records replace.
DROP INDEX generation_usage_owner_idx;

DROP INDEX generation_usage_owner_purpose_idx;
