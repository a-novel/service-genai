-- One row per attempt, written in the transaction of the fenced transition that ends it, so no attempt
-- records two answers. The response is not sent back: the caller holds it.
INSERT INTO
  generation_attempts (generation_id, attempt, response)
VALUES
  (?0, ?1, ?2)
RETURNING
  generation_id,
  attempt,
  created_at;
