-- Takes the next attempt and records that its call may be sent, with the payload to send. The
-- conditions read the row's own state, so of two concurrent checks only the first to commit matches.
--
-- The attempt's first start is kept across released starts, so their retries are bounded from it. One
-- instant serves every timestamp.
UPDATE generations
SET
  attempt = attempt + 1,
  start_requested_at = intent.at,
  first_start_requested_at = coalesce(first_start_requested_at, intent.at),
  updated_at = intent.at
FROM
  (
    SELECT
      clock_timestamp() AS at
  ) AS intent
WHERE
  generations.id = ?0
  AND generations.status = 'pending'
  AND generations.start_requested_at IS NULL
  AND generations.cancel_requested_at IS NULL
  AND generations.run_at <= intent.at
RETURNING
  generations.*,
  (
    SELECT
      payload
    FROM
      generation_payloads
    WHERE
      generation_id = generations.id
  ) AS payload;
