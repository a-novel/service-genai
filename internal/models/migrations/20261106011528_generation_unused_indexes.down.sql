-- Each index builds concurrently, on its own, once a leftover of its name from a failed build is
-- dropped.
DROP INDEX CONCURRENTLY IF EXISTS generations_provider_call_idx;

--bun:split
CREATE INDEX CONCURRENTLY generations_provider_call_idx ON generations (provider, provider_call_id)
WHERE
  status = 'running';

--bun:split
DROP INDEX CONCURRENTLY IF EXISTS generations_check_idx;

--bun:split
CREATE INDEX CONCURRENTLY generations_check_idx ON generations (checked_at)
WHERE
  status IN ('pending', 'running');
