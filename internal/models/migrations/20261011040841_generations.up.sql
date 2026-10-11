-- generations holds the caller's payload and generation_attempts the provider's answers. Both are
-- user content, so both are purged on a retention schedule, the attempts with their generation.
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
  /* The service the payload is built for. Every attempt runs on it. */
  provider text NOT NULL CHECK (provider <> ''),
  /* Digest of the owner, the provider, the mode, the payload and the variant, computed by the
  service, so a caller recovers a generation by resending its request. */
  request_key bytea NOT NULL,
  /* The provider's own request, built by the caller. json keeps it as sent, where jsonb would
  reorder its keys before it reaches the provider. */
  payload json NOT NULL CHECK (json_typeof(payload) = 'object'),
  error text CHECK (
    error IS NULL
    OR error <> ''
  ),
  status generation_status NOT NULL DEFAULT 'pending',
  /* Numbers the provider calls this generation may have paid for. Recording start intent takes the
  next number, so each attempt names at most one call and one attempt row. */
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

-- A failed or cancelled generation leaves the index, so resending its request runs it again from
-- scratch instead of returning the failure.
CREATE UNIQUE INDEX generations_request_key_idx ON generations (request_key)
WHERE
  status NOT IN ('failed', 'cancelled');

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

-- The provider's answer to each attempt. One row per attempt, because a retry or a cancel can be
-- billed too, and the caller reads what each one consumed from its response.
CREATE TABLE generation_attempts (
  generation_id uuid NOT NULL REFERENCES generations (id) ON DELETE CASCADE,
  /* One-based: a row exists only once a call has been answered. */
  attempt smallint NOT NULL CHECK (attempt >= 1),
  /* The provider's final answer, as sent: the finished response, or the error body of a rejected
  request. */
  response json NOT NULL,
  created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT generation_attempts_pkey PRIMARY KEY (generation_id, attempt)
);
