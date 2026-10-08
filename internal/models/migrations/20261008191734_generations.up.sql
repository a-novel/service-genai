-- generations holds user content and is purged on a retention schedule. generation_usage holds
-- none and is kept, so the consumption record outlives the material it describes. That is why no
-- foreign key joins them: the purge deletes the parent while the usage row must survive.
CREATE TYPE generation_status AS ENUM(
  'pending',
  'running',
  'succeeded',
  'failed',
  'cancelled'
);

-- Timestamps are full precision and written with clock_timestamp(): two transitions can land in the
-- same second, and CURRENT_TIMESTAMP freezes at transaction start. Keys are uuidv7 for index
-- locality under insert churn.
--
-- No process owns a row. Every transition is one conditional UPDATE on the row's own state, so
-- whichever check commits first wins and the others match nothing.
CREATE TABLE generations (
  id uuid PRIMARY KEY NOT NULL DEFAULT uuidv7(),
  /* The user this acts for. Reads are scoped by it inside the statement, so a caller that omits it
  scans nothing rather than another owner's row. */
  owner_id uuid NOT NULL,
  purpose text NOT NULL CHECK (purpose <> ''),
  idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
  /* Digest of the request. A key reused with different content is a conflict, not a replay. */
  request_fingerprint bytea NOT NULL,
  /* The provider request, forwarded verbatim. User content, which is why this table is purged. */
  request jsonb NOT NULL CHECK (jsonb_typeof(request) = 'object'),
  output jsonb CHECK (
    output IS NULL
    OR jsonb_typeof(output) = 'object'
  ),
  error text CHECK (
    error IS NULL
    OR error <> ''
  ),
  status generation_status NOT NULL DEFAULT 'pending',
  /* Numbers the provider calls this generation may have paid for. Recording start intent takes the
  next number, so each attempt names at most one call and one usage row. */
  attempt smallint NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  max_attempts smallint NOT NULL DEFAULT 1 CHECK (max_attempts >= 1),
  /* Earliest time the next start may be sent, pushed back after a rate limit or a retryable failure. */
  run_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  /* Set before a start is sent. From then on the provider may hold paid work for this attempt, so
  nothing starts it again. */
  start_requested_at timestamp with time zone,
  /* The provider's identifier for the attempt's operation, which every later check reads back. */
  provider_call_id text CHECK (
    provider_call_id IS NULL
    OR provider_call_id <> ''
  ),
  cancel_requested_at timestamp with time zone,
  /* When a check last looked at this generation. The sweep picks the stalest, and a read elects
  at most one check per interval through it. */
  checked_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  settled_at timestamp with time zone,
  expires_at timestamp with time zone,
  -- A pending generation has no provider operation yet, and a running one always has.
  CONSTRAINT generations_call_matches_status CHECK (
    status NOT IN ('pending', 'running')
    OR (status = 'running') = (provider_call_id IS NOT NULL)
  ),
  CONSTRAINT generations_terminal_fields CHECK (
    (status IN ('succeeded', 'failed', 'cancelled')) = (
      settled_at IS NOT NULL
      AND expires_at IS NOT NULL
    )
  )
);

-- Scoped to the owner alone. The caller owns its own key space.
CREATE UNIQUE INDEX generations_idempotency_idx ON generations (owner_id, idempotency_key);

-- Partial, so it does not grow as settled rows accumulate.
CREATE INDEX generations_check_idx ON generations (checked_at)
WHERE
  status IN ('pending', 'running');

CREATE INDEX generations_retention_idx ON generations (expires_at)
WHERE
  expires_at IS NOT NULL;

-- Every check rewrites checked_at. Under the defaults, dead tuples from that churn accumulate faster
-- than the table is vacuumed and the sweep slows with no other symptom.
ALTER TABLE generations
SET
  (
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 25,
    autovacuum_analyze_scale_factor = 0.02
  );

-- What each attempt consumed. One row per attempt, because a retry burns tokens twice and both have
-- to be recorded. owner_id and purpose are duplicated rather than joined because the row they would
-- join to is purged.
CREATE TABLE generation_usage (
  generation_id uuid NOT NULL,
  /* One-based: a row exists only once a call has been sent. */
  attempt smallint NOT NULL CHECK (attempt >= 1),
  owner_id uuid NOT NULL,
  purpose text NOT NULL CHECK (purpose <> ''),
  /* What actually ran, read back off the provider's response rather than off the request: a
  provider may serve a different snapshot than the one asked for. */
  provider text NOT NULL CHECK (provider <> ''),
  model text NOT NULL CHECK (model <> ''),
  /* Totals include the detail counts below, mirroring the provider's own accounting. */
  input_tokens bigint NOT NULL CHECK (input_tokens >= 0),
  cached_input_tokens bigint NOT NULL DEFAULT 0 CHECK (cached_input_tokens >= 0),
  output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
  reasoning_tokens bigint NOT NULL DEFAULT 0 CHECK (reasoning_tokens >= 0),
  created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT generation_usage_pkey PRIMARY KEY (generation_id, attempt),
  -- A count outside its total means the provider's numbers were mapped wrong.
  CONSTRAINT generation_usage_cached_within_input CHECK (cached_input_tokens <= input_tokens),
  CONSTRAINT generation_usage_reasoning_within_output CHECK (reasoning_tokens <= output_tokens)
);

CREATE INDEX generation_usage_owner_idx ON generation_usage (owner_id, created_at DESC);

CREATE INDEX generation_usage_owner_purpose_idx ON generation_usage (owner_id, purpose, created_at DESC);
