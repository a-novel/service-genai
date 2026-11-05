-- One row per attempt, written in the transaction of the fenced transition that ends it, so no attempt
-- records two answers. The response is not sent back: the caller holds it.
--
-- A generation runs one call, recorded as attempt 1. No other attempt of an unsettled generation
-- exists: v0.9.0 inserts attempt 1 and starts each generation once, and its 2-hour run ceiling settled
-- every generation an earlier release left mid-retry.
INSERT INTO
  generation_attempts (generation_id, attempt, response)
VALUES
  (?0, 1, ?1)
RETURNING
  generation_id,
  attempt,
  created_at;
