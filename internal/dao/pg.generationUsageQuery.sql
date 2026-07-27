-- Consumption for one owner over a window, grouped by what a downstream layer needs to price it:
-- the purpose it was spent on and the model that actually billed.
--
-- The window is not optional. This table is never purged, so an unbounded scan grows without limit
-- and the caller has no way to know it asked for one.
--
-- The optional filters narrow rather than drive: the owner and window predicate is what the index
-- serves, and the residual applies to the few rows that survive it.
SELECT
  purpose,
  model,
  sum(input_tokens)::bigint AS input_tokens,
  sum(cached_input_tokens)::bigint AS cached_input_tokens,
  sum(output_tokens)::bigint AS output_tokens,
  sum(reasoning_tokens)::bigint AS reasoning_tokens,
  count(*)::bigint AS attempts
FROM
  generation_usage
WHERE
  owner_id = ?0
  AND created_at >= ?1
  AND created_at < ?2
  AND (
    ?3 = ''
    OR purpose = ?3
  )
  AND (
    ?4 = ''
    OR model = ?4
  )
GROUP BY
  purpose,
  model
ORDER BY
  purpose,
  model;
