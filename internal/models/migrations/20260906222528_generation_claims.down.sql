-- Stop workers before rollback: removing intent also removes the evidence needed for safe recovery.
ALTER TABLE generations
DROP COLUMN IF EXISTS start_requested_at,
DROP COLUMN IF EXISTS claim_token;
