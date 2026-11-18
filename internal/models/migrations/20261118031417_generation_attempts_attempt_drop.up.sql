-- A generation holds one answer, so its row needs no attempt number: v0.12 stopped naming the column,
-- and every row the purge has not deleted holds 1. The key becomes the generation alone. The table
-- keeps a few hours of answers, so its new key builds under the table's lock in a moment.
ALTER TABLE generation_attempts
DROP CONSTRAINT generation_attempts_pkey,
DROP COLUMN attempt,
ADD CONSTRAINT generation_attempts_pkey PRIMARY KEY (generation_id);
