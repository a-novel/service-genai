-- Input tokens a call wrote to the provider's prompt cache. Billed above the plain input rate, so a
-- caller pricing usage needs them apart from the total.
ALTER TABLE generation_usage
ADD COLUMN cache_write_tokens bigint NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0);
