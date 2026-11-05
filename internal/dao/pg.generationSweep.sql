-- One page of the unsettled generations, in id order from after the given one, so a sweep reads each
-- once however many pages it takes. The read takes no lock and writes nothing: a check writes only to
-- settle. Each row carries the database clock, which its check measures the generation's ages from.
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
  status IN ('pending', 'running')
  AND id > ?0
ORDER BY
  id
LIMIT
  ?1;
