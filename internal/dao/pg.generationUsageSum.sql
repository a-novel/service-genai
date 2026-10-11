-- One owner's usage over a rolling window, across every purpose when none is given. A null purpose
-- is a literal by the time the planner sees it, so each form uses its own index. The window starts
-- from a stable clock, so the index scan stops at it.
SELECT
  coalesce(sum(input_tokens), 0)::bigint AS input_tokens,
  coalesce(sum(output_tokens), 0)::bigint AS output_tokens
FROM
  generation_usage
WHERE
  owner_id = ?0
  AND (
    ?1::text IS NULL
    OR purpose = ?1
  )
  AND created_at > CURRENT_TIMESTAMP - make_interval(secs => ?2);
