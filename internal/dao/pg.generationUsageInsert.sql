-- One row per answered attempt. The stop of an orphaned call records its usage outside any settle,
-- and a check may settle the same call once its late call id write lands: the row already there wins.
INSERT INTO
  generation_usage (
    generation_id,
    attempt,
    owner_id,
    purpose,
    provider,
    model,
    class,
    tier,
    input_tokens,
    cached_input_tokens,
    cache_write_tokens,
    output_tokens,
    reasoning_tokens,
    started_at,
    completed_at
  )
VALUES
  (
    ?0,
    ?1,
    ?2,
    ?3,
    ?4,
    ?5,
    ?6,
    ?7,
    ?8,
    ?9,
    ?10,
    ?11,
    ?12,
    ?13,
    ?14
  )
ON CONFLICT (generation_id, attempt) DO NOTHING
RETURNING
  *;
