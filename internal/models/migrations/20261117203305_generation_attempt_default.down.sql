ALTER TABLE generation_usage
ALTER COLUMN attempt
DROP DEFAULT;

ALTER TABLE generation_attempts
ALTER COLUMN attempt
DROP DEFAULT;
