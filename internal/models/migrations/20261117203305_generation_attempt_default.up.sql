-- A generation holds one answer and one Usage Record, so the service stops naming the attempt
-- columns, which are always 1. They default to 1 until a later release drops them, once no running
-- release names them. A new default changes only the catalog.
ALTER TABLE generation_attempts
ALTER COLUMN attempt
SET DEFAULT 1;

ALTER TABLE generation_usage
ALTER COLUMN attempt
SET DEFAULT 1;
