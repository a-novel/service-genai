-- What each answered attempt consumed, kept for good: caps sum it over rolling windows, and it is
-- the platform's long-term usage record. It holds no user content, and no foreign key joins it to
-- generations, because the purge deletes a generation while its usage must survive.
CREATE TABLE generation_usage (
  generation_id uuid NOT NULL,
  /* One-based: a row exists only once a call has been answered. */
  attempt smallint NOT NULL CHECK (attempt >= 1),
  owner_id uuid NOT NULL,
  purpose text NOT NULL CHECK (purpose <> ''),
  provider text NOT NULL CHECK (provider <> ''),
  /* What actually ran, read back off the provider's response rather than off the request: a
  provider may serve a different snapshot than the one asked for. Null when the provider names
  none, since a refused insert would keep the attempt from ever settling. */
  model text CHECK (
    model IS NULL
    OR model <> ''
  ),
  /* The provider's processing class, such as OpenAI's flex or priority, which sets the unit price.
  Null when the provider reports none. */
  service_tier text CHECK (
    service_tier IS NULL
    OR service_tier <> ''
  ),
  /* Totals include the detail counts below, mirroring the provider's own accounting. */
  input_tokens bigint NOT NULL CHECK (input_tokens >= 0),
  cached_input_tokens bigint NOT NULL DEFAULT 0 CHECK (cached_input_tokens >= 0),
  cache_write_tokens bigint NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
  output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
  reasoning_tokens bigint NOT NULL DEFAULT 0 CHECK (reasoning_tokens >= 0),
  created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT generation_usage_pkey PRIMARY KEY (generation_id, attempt),
  -- A count outside its total means the provider's numbers were mapped wrong.
  CONSTRAINT generation_usage_cached_within_input CHECK (cached_input_tokens <= input_tokens),
  CONSTRAINT generation_usage_reasoning_within_output CHECK (reasoning_tokens <= output_tokens)
);

-- Caps sum one owner's usage over a recent window, across every purpose or within one.
CREATE INDEX generation_usage_owner_idx ON generation_usage (owner_id, created_at);

CREATE INDEX generation_usage_owner_purpose_idx ON generation_usage (owner_id, purpose, created_at);
