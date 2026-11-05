-- The service inserts generations without an attempt: each runs one call, recorded as attempt 1. A
-- v0.9.0 replica still checks those rows during a rollout. It records a call's answer under the
-- attempt it reads off the row, which generation_attempts_attempt_check refuses below 1, so the
-- column defaults to 1. A new default rewrites no row: the lock it takes on generations lasts as long
-- as the catalog update.
ALTER TABLE generations
ALTER COLUMN attempt
SET DEFAULT 1;
