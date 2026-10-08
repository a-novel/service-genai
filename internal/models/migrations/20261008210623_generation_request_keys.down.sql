-- Keys cannot be recovered from digests; each generation gets one unique to its row.
ALTER TABLE generations
ADD COLUMN IF NOT EXISTS idempotency_key text CHECK (idempotency_key <> ''),
ADD COLUMN IF NOT EXISTS request_fingerprint bytea;

UPDATE generations
SET
  idempotency_key = id::text,
  request_fingerprint = request_key;

ALTER TABLE generations
ALTER COLUMN idempotency_key
SET NOT NULL,
ALTER COLUMN request_fingerprint
SET NOT NULL;

DROP INDEX IF EXISTS generations_request_key_idx;

CREATE UNIQUE INDEX generations_idempotency_idx ON generations (owner_id, idempotency_key);

ALTER TABLE generations
DROP COLUMN IF EXISTS request_key;
