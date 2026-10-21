-- One owner's usage over each rolling window, across every purpose when none is given. One round trip
-- answers every window, and each window gets one row, in the order given. Each window reads its own
-- range of the owner's index. A null purpose is a literal by the time the planner sees it, so each
-- form uses its own index. The windows start from a stable clock, so each index scan stops at the
-- start of its window.
SELECT
  coalesce(sum(generation_usage.input_tokens), 0)::bigint AS input_tokens,
  coalesce(sum(generation_usage.output_tokens), 0)::bigint AS output_tokens
FROM
  unnest(?2::double precision[]) WITH ORDINALITY AS windows (seconds, position)
  LEFT JOIN generation_usage ON generation_usage.owner_id = ?0
  AND (
    ?1::text IS NULL
    OR generation_usage.purpose = ?1
  )
  AND generation_usage.created_at > CURRENT_TIMESTAMP - make_interval(secs => windows.seconds)
GROUP BY
  windows.position
ORDER BY
  windows.position
LIMIT
  ?3;
