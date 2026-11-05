-- A generation inserted without an attempt, then answered under the attempt a v0.9.0 check reads off
-- the row: the answer passes generation_attempts_attempt_check only because the column defaults to 1.
-- Its payload row lets the roundtrip roll back past the migration that moved payloads out of
-- generations.
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
    '01999999-0000-7000-8000-000000000006',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'openai',
    '\x06'::bytea,
    'succeeded',
    clock_timestamp(),
    'resp_fixture_default_attempt',
    clock_timestamp(),
    clock_timestamp() + interval '6 hours'
  );

INSERT INTO
  generation_attempts (generation_id, attempt, response)
SELECT
  id,
  attempt,
  '{"id": "resp_fixture_default_attempt", "status": "completed"}'::json
FROM
  generations
WHERE
  id = '01999999-0000-7000-8000-000000000006';

INSERT INTO
  generation_payloads (generation_id, payload)
VALUES
  (
    '01999999-0000-7000-8000-000000000006',
    '{"model": "fixture"}'::json
  );
