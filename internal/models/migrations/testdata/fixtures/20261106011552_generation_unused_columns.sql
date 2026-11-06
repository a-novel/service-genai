-- A generation settled with its answer, inserted without the dropped columns. The down of the drop
-- gives it their defaults.
INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    provider,
    request_key,
    status,
    start_requested_at,
    provider_call_id,
    settled_at,
    expires_at
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000008',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'openai',
    '\x08'::bytea,
    'succeeded',
    clock_timestamp(),
    'resp_fixture_lean',
    clock_timestamp(),
    clock_timestamp() + interval '6 hours'
  );

INSERT INTO
  generation_attempts (generation_id, attempt, response)
VALUES
  (
    '01999999-0000-7000-8000-000000000008',
    1,
    '{"id": "resp_fixture_lean", "status": "completed"}'::json
  );
