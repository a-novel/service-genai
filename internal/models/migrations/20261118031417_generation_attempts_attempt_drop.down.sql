-- Every answer comes back as attempt 1.
ALTER TABLE generation_attempts
DROP CONSTRAINT generation_attempts_pkey,
ADD COLUMN attempt smallint NOT NULL DEFAULT 1 CHECK (attempt >= 1),
ADD CONSTRAINT generation_attempts_pkey PRIMARY KEY (generation_id, attempt);
