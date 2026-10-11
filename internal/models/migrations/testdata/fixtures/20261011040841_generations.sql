-- Seeds one row per lifecycle shape so a later migration carries real data over: unstarted, started
-- without an id yet, running, and settled with its attempt.
INSERT INTO
  generations (
    id,
    owner_id,
    provider,
    request_key,
    payload,
    status,
    attempt,
    start_requested_at,
    provider_call_id
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'openai',
    '\x01'::bytea,
    '{"model": "fixture"}'::json,
    'pending',
    0,
    NULL,
    NULL
  ),
  (
    '01999999-0000-7000-8000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    'openai',
    '\x02'::bytea,
    '{"model": "fixture"}'::json,
    'pending',
    1,
    clock_timestamp(),
    NULL
  ),
  (
    '01999999-0000-7000-8000-000000000003',
    '00000000-0000-0000-0000-000000000001',
    'openai',
    '\x03'::bytea,
    '{"model": "fixture"}'::json,
    'running',
    1,
    clock_timestamp(),
    'resp_fixture'
  );

INSERT INTO
  generations (
    id,
    owner_id,
    provider,
    request_key,
    payload,
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
    'openai',
    '\x04'::bytea,
    '{"model": "fixture"}'::json,
    'succeeded',
    1,
    clock_timestamp(),
    'resp_fixture_done',
    clock_timestamp(),
    clock_timestamp() + interval '6 hours'
  );

INSERT INTO
  generation_attempts (generation_id, attempt, response)
VALUES
  (
    '01999999-0000-7000-8000-000000000004',
    1,
    '{"id": "resp_fixture_done", "status": "completed"}'::json
  );
