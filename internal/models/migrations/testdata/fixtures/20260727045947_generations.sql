-- A fixture runs after its matching migration and seeds data the next migration must carry over.
--
-- Two rows deliberately: a pending generation with no ledger, and a settled one whose ledger row
-- outlives it. Together they cover both sides of the terminal-fields constraint and prove the
-- ledger stands on its own, with no foreign key holding it to its parent.
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
  generation_ledger (
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
    reasoning_tokens,
    input_price_per_mtoken,
    cached_input_price_per_mtoken,
    output_price_per_mtoken,
    price_book_version,
    currency,
    cost
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
    100,
    1.25,
    0.125,
    10,
    'fixture',
    'USD',
    0.006
  );
