-- The index builds concurrently, on its own, once a leftover of its name from a failed build is
-- dropped.
DROP INDEX CONCURRENTLY IF EXISTS generation_usage_owner_idx;

--bun:split
CREATE INDEX CONCURRENTLY generation_usage_owner_idx ON generation_usage (owner_id, created_at) INCLUDE (input_tokens, output_tokens);
