-- A running generation without a payload row, as the service inserts it. The down of the drop gives
-- it an empty payload.
INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    provider,
    request_key,
    status,
    start_requested_at,
    provider_call_id
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000007',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'openai',
    '\x07'::bytea,
    'running',
    clock_timestamp(),
    'resp_fixture_no_payload'
  );
