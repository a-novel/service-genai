-- Claims rotate independently of the inference attempt retained during provider observation.
ALTER TABLE generations
ADD COLUMN claim_token uuid,
ADD COLUMN start_requested_at timestamp(0) with time zone;

-- Deploy with old workers and reapers stopped. Existing attempts without an operation ID cannot
-- prove whether the provider accepted work; retaining uncertainty prevents a replacement charge.
UPDATE generations
SET
  claim_token = CASE
    WHEN status = 'running' THEN uuidv7()
  END,
  start_requested_at = CASE
    WHEN attempt > 0 THEN updated_at
  END
WHERE
  status IN ('pending', 'running');
