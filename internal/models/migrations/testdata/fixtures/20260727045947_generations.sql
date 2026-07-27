-- Seeds data the next migration must carry over. Two rows cover both sides of the terminal-fields
-- constraint, and the settled one's usage row proves usage stands without its parent.
INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    profile,
    idempotency_key,
    request_fingerprint,
    request,
    status
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'draft',
    'fixture-pending',
    '\x00'::bytea,
    '{"instructions": "fixture"}'::jsonb,
    'pending'
  );

INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
    profile,
    idempotency_key,
    request_fingerprint,
    request,
    output,
    status,
    attempt,
    settled_at,
    expires_at
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'draft',
    'fixture-succeeded',
    '\x01'::bytea,
    '{"instructions": "fixture"}'::jsonb,
    '{"text": "fixture output"}'::jsonb,
    'succeeded',
    1,
    clock_timestamp(),
    clock_timestamp() + interval '7 days'
  );

INSERT INTO
  generation_usage (
    generation_id,
    attempt,
    owner_id,
    purpose,
    profile,
    provider,
    model,
    input_tokens,
    cached_input_tokens,
    output_tokens,
    reasoning_tokens
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000002',
    1,
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'draft',
    'openai',
    'fixture-model',
    1000,
    200,
    500,
    100
  );
