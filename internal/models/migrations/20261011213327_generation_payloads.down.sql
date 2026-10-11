ALTER TABLE generations
ADD COLUMN payload json CONSTRAINT generations_payload_check CHECK (json_typeof(payload) = 'object');

UPDATE generations
SET
  payload = generation_payloads.payload
FROM
  generation_payloads
WHERE
  generation_payloads.generation_id = generations.id;

ALTER TABLE generations
ALTER COLUMN payload
SET NOT NULL;

DROP TABLE generation_payloads;
