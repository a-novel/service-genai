-- The caller's payload, apart from its generation: only a start reads it, while every poll and check
-- reads or rewrites the generation row, which stays narrow without it. The purge takes it with its
-- generation.
CREATE TABLE generation_payloads (
  generation_id uuid PRIMARY KEY NOT NULL REFERENCES generations (id) ON DELETE CASCADE,
  /* The provider's own request, built by the caller. json keeps it as sent, where jsonb would
  reorder its keys before it reaches the provider. */
  payload json NOT NULL CHECK (json_typeof(payload) = 'object')
);

INSERT INTO
  generation_payloads (generation_id, payload)
SELECT
  id,
  payload
FROM
  generations;

ALTER TABLE generations
DROP COLUMN payload;
