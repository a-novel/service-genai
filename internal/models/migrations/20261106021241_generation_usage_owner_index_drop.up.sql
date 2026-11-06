-- Caps sum usage per purpose, off the index on owner, purpose and time, so no query reads the index
-- on owner and time. The drop runs concurrently, on its own, so writes to generation_usage go on. A
-- drop that gives up on a lock leaves the index invalid, and the migrations job runs the file again
-- to finish it.
DROP INDEX CONCURRENTLY IF EXISTS generation_usage_owner_idx;
