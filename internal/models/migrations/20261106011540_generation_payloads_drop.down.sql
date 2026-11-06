-- The payloads come back as empty objects, one per generation, since the service stores none. Rolling
-- back past the migration that moved payloads out of generations needs one for each generation.
CREATE TABLE generation_payloads (
  generation_id uuid PRIMARY KEY NOT NULL REFERENCES generations (id) ON DELETE CASCADE,
  payload json NOT NULL CHECK (json_typeof(payload) = 'object')
);

INSERT INTO
  generation_payloads (generation_id, payload)
SELECT
  id,
  '{}'
FROM
  generations;
