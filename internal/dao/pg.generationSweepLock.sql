-- Takes the sweep's leader role for this session, or answers false at once when another session holds
-- it. The lock is a session's, so it holds until the session releases it or ends.
SELECT
  pg_try_advisory_lock(?0, ?1) AS locked;
