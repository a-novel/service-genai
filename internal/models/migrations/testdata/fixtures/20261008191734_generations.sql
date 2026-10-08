-- Seeds one row per lifecycle shape so later migrations carry real data over: unstarted, started
-- without an id yet, running, and settled with its usage.
INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    idempotency_key,
    request_fingerprint,
    request,
    status,
    attempt,
    start_requested_at,
    provider_call_id
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'fixture-pending',
    '\x00'::bytea,
    '{"instructions": "fixture"}'::jsonb,
    'pending',
    0,
    NULL,
    NULL
  ),
  (
    '01999999-0000-7000-8000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'fixture-starting',
    '\x00'::bytea,
    '{"instructions": "fixture"}'::jsonb,
    'pending',
    1,
    clock_timestamp(),
    NULL
  ),
  (
    '01999999-0000-7000-8000-000000000003',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'fixture-running',
    '\x00'::bytea,
    '{"instructions": "fixture"}'::jsonb,
    'running',
    1,
    clock_timestamp(),
    'resp_fixture'
  );

INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    idempotency_key,
    request_fingerprint,
    request,
    output,
    status,
    attempt,
    start_requested_at,
    provider_call_id,
    settled_at,
    expires_at
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000004',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'fixture-succeeded',
    '\x01'::bytea,
    '{"instructions": "fixture"}'::jsonb,
    '{"text": "fixture output"}'::jsonb,
    'succeeded',
    1,
    clock_timestamp(),
    'resp_fixture_done',
    clock_timestamp(),
    clock_timestamp() + interval '6 hours'
  );

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
  (
    '01999999-0000-7000-8000-000000000004',
    1,
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'openai',
    'fixture-model',
    1000,
    200,
    500,
    100
  );
