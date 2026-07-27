-- Terminal transition, guarded on the claim holder. expires_at is stamped here: it is what the
-- retention purge reads, and the terminal-fields constraint refuses a settle that omits it.
UPDATE generations
SET
  status = ?2,
  output = ?3,
  error = ?4,
  lease_expires_at = NULL,
  settled_at = clock_timestamp(),
  expires_at = clock_timestamp() + make_interval(secs => ?5),
  updated_at = clock_timestamp()
WHERE
  id = ?0
  AND claimed_by = ?1
  AND status = 'running'
RETURNING
  *;
