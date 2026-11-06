-- The values come back as their defaults. checked_at and run_at first take a stable default, which
-- PostgreSQL stores in the catalog without rewriting the table, so every restored row takes the
-- down's instant. Rows inserted after it take clock_timestamp() again. The checks scan the table
-- under its lock, which the purge keeps to a few hours of generations.
ALTER TABLE generations
ADD COLUMN checked_at timestamp with time zone NOT NULL DEFAULT now(),
ADD COLUMN run_at timestamp with time zone NOT NULL DEFAULT now(),
ADD COLUMN attempt smallint NOT NULL DEFAULT 1 CONSTRAINT generations_attempt_check CHECK (attempt >= 0),
ADD COLUMN max_attempts smallint NOT NULL DEFAULT 1 CONSTRAINT generations_max_attempts_check CHECK (max_attempts >= 1),
ADD COLUMN first_start_requested_at timestamp with time zone;

ALTER TABLE generations
ALTER COLUMN checked_at
SET DEFAULT clock_timestamp(),
ALTER COLUMN run_at
SET DEFAULT clock_timestamp();
