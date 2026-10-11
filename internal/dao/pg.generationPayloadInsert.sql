-- Run once the generation is admitted, so neither a replay nor a refusal sends the payload over. Only
-- the key comes back: the caller already holds the payload.
INSERT INTO
  generation_payloads (generation_id, payload)
VALUES
  (?0, ?1)
RETURNING
  generation_id;
