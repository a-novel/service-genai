-- One row per attempt. A replay of the same settle must not double-count, so a row that already
-- exists for this attempt wins.
INSERT INTO
  generation_usage (
    generation_id,
    attempt,
    owner_id,
    purpose,
    provider,
    model,
    input_tokens,
    cached_input_tokens,
    output_tokens,
    reasoning_tokens
  )
VALUES
  (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)
ON CONFLICT (generation_id, attempt) DO NOTHING
RETURNING
  *;
