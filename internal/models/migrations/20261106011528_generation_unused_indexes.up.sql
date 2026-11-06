-- No query reads either index. Each drop runs concurrently, on its own, so writes to generations go
-- on. A drop that gives up on a lock leaves its index invalid, and the migrations job runs the file
-- again to finish it.
DROP INDEX CONCURRENTLY IF EXISTS generations_check_idx;

--bun:split
DROP INDEX CONCURRENTLY IF EXISTS generations_provider_call_idx;
