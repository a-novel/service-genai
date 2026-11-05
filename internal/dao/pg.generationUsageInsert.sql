-- One row per answered attempt. The stop of an orphaned call records its usage outside any settle,
-- and a check may settle the same call once its late call id write lands: the row already there wins.
--
-- A generation runs one call, recorded as attempt 1. No other attempt of an unsettled generation
-- exists: v0.9.0 inserts attempt 1 and starts each generation once, and its 2-hour run ceiling settled
-- every generation an earlier release left mid-retry.
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
    1,
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
    ?13
  )
ON CONFLICT (generation_id, attempt) DO NOTHING
RETURNING
  *;
