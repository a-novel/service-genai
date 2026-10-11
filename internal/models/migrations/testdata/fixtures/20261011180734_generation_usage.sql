-- One answered attempt, whose generation was already purged: usage outlives it.
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
    '01999999-0000-7000-8000-0000000000ff',
    1,
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'openai',
    'fixture-model-2026-01-01',
    'high',
    'default',
    1000,
    200,
    300,
    500,
    100,
    clock_timestamp() - interval '2 minutes',
    clock_timestamp() - interval '1 minute'
  );
