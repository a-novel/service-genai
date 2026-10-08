-- A generation is identified by its request and owner, so a caller recovers it by resending rather
-- than by keeping a key. The key is a digest computed by the service; generations recorded before it
-- existed get a value unique to their row.
ALTER TABLE generations
ADD COLUMN request_key bytea;

UPDATE generations
SET
  request_key = sha256(convert_to(id::text, 'UTF8'));

ALTER TABLE generations
ALTER COLUMN request_key
SET NOT NULL;

DROP INDEX generations_idempotency_idx;

-- A failed or cancelled generation leaves the index, so resending its request runs it again from
-- scratch instead of returning the failure.
CREATE UNIQUE INDEX generations_request_key_idx ON generations (request_key)
WHERE
  status NOT IN ('failed', 'cancelled');

ALTER TABLE generations
DROP COLUMN idempotency_key,
DROP COLUMN request_fingerprint;
