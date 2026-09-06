-- Seeds data the next migration must carry over. Two rows cover both sides of the terminal-fields
-- constraint, and the settled one's usage row proves usage stands without its parent.
INSERT INTO
  generations (
    id,
    owner_id,
    purpose,
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
    'openai',
    'fixture-model',
    1000,
    200,
    500,
    100
  );

-- Live and stranded legacy attempts need conservative Start evidence after the ownership migration.
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
    max_attempts,
    claimed_by,
    lease_expires_at
  )
VALUES
  (
    '01999999-0000-7000-8000-000000000003',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'fixture-running',
    '\x00'::bytea,
    '{}'::jsonb,
    'running',
    1,
    3,
    'legacy-worker',
    clock_timestamp() + interval '5 minutes'
  ),
  (
    '01999999-0000-7000-8000-000000000004',
    '00000000-0000-0000-0000-000000000001',
    'studio.generation',
    'fixture-stranded',
    '\x00'::bytea,
    '{}'::jsonb,
    'pending',
    1,
    3,
    NULL,
    NULL
  );
