-- One row per answered attempt. A replay of the same settle must not count it twice, so a row that
-- already exists for this attempt wins.
INSERT INTO
  generation_usage (
    generation_id,
    attempt,
    owner_id,
    purpose,
    provider,
    model,
    service_tier,
    input_tokens,
    cached_input_tokens,
    cache_write_tokens,
    output_tokens,
    reasoning_tokens
  )
VALUES
  (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)
ON CONFLICT (generation_id, attempt) DO NOTHING
RETURNING
  *;
