-- Written in the transaction of the fenced transition that settles the generation, so no generation
-- records two answers. The response is not sent back: the caller holds it.
INSERT INTO
  generation_attempts (generation_id, response)
VALUES
  (?0, ?1)
RETURNING
  generation_id,
  created_at;
