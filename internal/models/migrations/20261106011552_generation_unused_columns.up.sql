-- Neither the service nor a v0.10 replica, which still serves while the migrations run, names these
-- columns. Each drop changes the catalog only, and takes the column's check and not-null constraints
-- with it.
ALTER TABLE generations
DROP COLUMN checked_at,
DROP COLUMN run_at,
DROP COLUMN attempt,
DROP COLUMN max_attempts,
DROP COLUMN first_start_requested_at;
