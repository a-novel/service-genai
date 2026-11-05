-- A generation failed with its class, so a later migration carries one over. Its payload row lets the
-- roundtrip roll back past the migration that moved payloads out of generations.
INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    provider,
    request_key,
    error,
    status,
    attempt,
    start_requested_at,
    first_start_requested_at,
    failure,
    settled_at,
    expires_at
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000005',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'openai',
    '\x05'::bytea,
    'the provider call could not be started',
    'failed',
    1,
    clock_timestamp(),
    clock_timestamp(),
    'not_started',
    clock_timestamp(),
    clock_timestamp() + interval '6 hours'
  );

INSERT INTO
  generation_payloads (generation_id, payload)
VALUES
  (
    '01999999-0000-7000-8000-000000000005',
    '{"model": "fixture"}'::json
  );
