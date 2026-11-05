-- The owner is in the predicate, not checked after the fact, so another owner's generation is
-- indistinguishable from one that does not exist and an identifier cannot be probed. The read carries
-- the database clock, which a check measures the generation's ages from.
SELECT
  id,
  owner_id,
  purpose,
  provider,
  request_key,
  error,
  failure,
  status,
  start_requested_at,
  provider_call_id,
  cancel_requested_at,
  traceparent,
  outcome_unknown_at,
  streamed,
  created_at,
  updated_at,
  settled_at,
  expires_at,
  clock_timestamp() AS read_at
FROM
  generations
WHERE
  id = ?0
  AND owner_id = ?1;
