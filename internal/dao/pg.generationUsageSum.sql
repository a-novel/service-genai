-- One owner's usage of one purpose over each rolling window. One round trip answers every window, and
-- each window gets one row, in the order given. Each window reads its own range of the index on
-- owner, purpose and time. The windows start from a stable clock, so each index scan stops at the
-- start of its window. The oldest record in a window tells when the window frees usage: it leaves the
-- window first.
SELECT
  coalesce(sum(generation_usage.input_tokens), 0)::bigint AS input_tokens,
  coalesce(sum(generation_usage.output_tokens), 0)::bigint AS output_tokens,
  min(generation_usage.created_at) AS oldest_at
FROM
  unnest(ARRAY[?2]::double precision[]) WITH ORDINALITY AS windows (seconds, position)
  LEFT JOIN generation_usage ON generation_usage.owner_id = ?0
  AND generation_usage.purpose = ?1
  AND generation_usage.created_at > CURRENT_TIMESTAMP - make_interval(secs => windows.seconds)
GROUP BY
  windows.position
ORDER BY
  windows.position
LIMIT
  ?3;
