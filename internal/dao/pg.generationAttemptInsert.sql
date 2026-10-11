-- One row per attempt. A replay of the same settle must not record the answer twice, so a row that
-- already exists for this attempt wins. The response is not sent back: the caller holds it.
INSERT INTO
  generation_attempts (generation_id, attempt, response)
VALUES
  (?0, ?1, ?2)
ON CONFLICT (generation_id, attempt) DO NOTHING
RETURNING
  generation_id,
  attempt,
  created_at;
