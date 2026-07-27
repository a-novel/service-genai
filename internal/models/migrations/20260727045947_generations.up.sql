-- generations holds user content and is purged on a retention schedule. generation_ledger holds
-- none and is kept, so the cost history outlives the material it describes. That is why no foreign
-- key joins them: the purge deletes the parent while the ledger row must survive.
CREATE TYPE generation_status AS ENUM(
  'pending',
  'running',
  'succeeded',
  'failed',
  'abandoned',
  'cancelled'
);

-- Timestamps are full precision and written with clock_timestamp(). A lease and a settle can land
-- in the same second, and CURRENT_TIMESTAMP freezes at transaction start. Keys are uuidv7 for index
-- locality under insert churn.
CREATE TABLE generations (
  id uuid PRIMARY KEY NOT NULL DEFAULT uuidv7(),
  /* The user this acts for, and the attribution key for its cost. Reads are scoped by it inside the
  statement, so a caller that omits it scans nothing rather than another owner's row. */
  owner_id uuid NOT NULL,
  /* Why the platform spent, from a closed vocabulary. */
  purpose text NOT NULL CHECK (purpose <> ''),
  /* The execution tier, resolved to a provider and model outside the database. */
  profile text NOT NULL CHECK (profile <> ''),
  /* Required: an unkeyed submission of a priced call is a bug, not a default. */
  idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
  /* Digest of the request. A key reused with different content is a conflict, not a replay; NOT
  NULL so that comparison cannot pass on a null stored side. */
  request_fingerprint bytea NOT NULL,
  /* User content, and the reason this table is purged. */
  request jsonb NOT NULL CHECK (jsonb_typeof(request) = 'object'),
  output jsonb CHECK (
    output IS NULL
    OR jsonb_typeof(output) = 'object'
  ),
  /* Serialised failure. Text because nothing queries inside it. */
  error text CHECK (
    error IS NULL
    OR error <> ''
  ),
  status generation_status NOT NULL DEFAULT 'pending',
  attempt smallint NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  /* One attempt is the right floor for a priced call. */
  max_attempts smallint NOT NULL DEFAULT 1 CHECK (max_attempts >= 1),
  run_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  lease_expires_at timestamp with time zone,
  claimed_by text CHECK (
    claimed_by IS NULL
    OR claimed_by <> ''
  ),
  /* Ships ahead of the endpoint that writes it: the claim predicate sits behind a partial index,
  and widening that predicate later means re-reasoning about the index on a live table. */
  cancel_requested_at timestamp with time zone,
  /* The provider's identifier for the operation in flight. A generation whose worker died
  re-attaches to it instead of paying for a second one. */
  provider_call_id text CHECK (
    provider_call_id IS NULL
    OR provider_call_id <> ''
  ),
  created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  settled_at timestamp with time zone,
  expires_at timestamp with time zone,
  -- Written as an equality so it binds both ways: a running row without a lease, and a lease on a
  -- row in any other status, are both rejected.
  CONSTRAINT generations_lease_matches_status CHECK (
    (status = 'running') = (lease_expires_at IS NOT NULL)
  ),
  CONSTRAINT generations_terminal_fields CHECK (
    (
      status IN ('succeeded', 'failed', 'abandoned', 'cancelled')
    ) = (
      settled_at IS NOT NULL
      AND expires_at IS NOT NULL
    )
  )
);

-- purpose is part of the key: without it one owner's keys collide across features, and a submission
-- from one feature is served another's answer.
CREATE UNIQUE INDEX generations_idempotency_idx ON generations (owner_id, purpose, idempotency_key);

-- Partial, so none of them grows as terminal rows accumulate.
CREATE INDEX generations_dispatch_idx ON generations (run_at)
WHERE
  status = 'pending';

CREATE INDEX generations_lease_idx ON generations (lease_expires_at)
WHERE
  status = 'running';

CREATE INDEX generations_retention_idx ON generations (expires_at)
WHERE
  expires_at IS NOT NULL;

CREATE INDEX generations_owner_idx ON generations (owner_id, created_at DESC);

-- Under the defaults, dead tuples from claim churn accumulate faster than the table is vacuumed and
-- the claim query slows with no other symptom.
ALTER TABLE generations
SET
  (
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 25,
    autovacuum_analyze_scale_factor = 0.02
  );

-- One row per attempt: a retry that burned tokens twice cost twice. owner_id, purpose and profile
-- are duplicated rather than joined because the row they would join to is purged.
CREATE TABLE generation_ledger (
  generation_id uuid NOT NULL,
  /* One-based: a row exists only once a run has begun. */
  attempt smallint NOT NULL CHECK (attempt >= 1),
  owner_id uuid NOT NULL,
  purpose text NOT NULL CHECK (purpose <> ''),
  profile text NOT NULL CHECK (profile <> ''),
  /* What actually ran. Recorded, not derived: a profile resolves to different models over time. */
  provider text NOT NULL CHECK (provider <> ''),
  model text NOT NULL CHECK (model <> ''),
  /* Totals include their detail counts below, mirroring the provider's accounting so a row reads
  against an invoice line directly. */
  input_tokens bigint NOT NULL CHECK (input_tokens >= 0),
  cached_input_tokens bigint NOT NULL DEFAULT 0 CHECK (cached_input_tokens >= 0),
  output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
  reasoning_tokens bigint NOT NULL DEFAULT 0 CHECK (reasoning_tokens >= 0),
  /* Rates in force when this attempt settled, per million tokens. Snapshotted rather than
  referenced: a later price change must not rewrite what a past call cost. */
  input_price_per_mtoken numeric(20, 12) NOT NULL CHECK (input_price_per_mtoken >= 0),
  cached_input_price_per_mtoken numeric(20, 12) NOT NULL CHECK (cached_input_price_per_mtoken >= 0),
  output_price_per_mtoken numeric(20, 12) NOT NULL CHECK (output_price_per_mtoken >= 0),
  /* Traces a disputed row to the commit that set its rates. */
  price_book_version text NOT NULL CHECK (price_book_version <> ''),
  /* ISO 4217, constrained to the shape so a second currency needs no migration. */
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  /* Money, so numeric: a float cannot represent a cent. */
  cost numeric(20, 8) NOT NULL CHECK (cost >= 0),
  created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT generation_ledger_pkey PRIMARY KEY (generation_id, attempt),
  -- A count outside its total means the usage was mapped wrong, which misprices silently.
  CONSTRAINT generation_ledger_cached_within_input CHECK (cached_input_tokens <= input_tokens),
  CONSTRAINT generation_ledger_reasoning_within_output CHECK (reasoning_tokens <= output_tokens)
);

-- The two slices the usage read surface serves.
CREATE INDEX generation_ledger_owner_idx ON generation_ledger (owner_id, created_at DESC);

CREATE INDEX generation_ledger_owner_purpose_idx ON generation_ledger (owner_id, purpose, created_at DESC);
