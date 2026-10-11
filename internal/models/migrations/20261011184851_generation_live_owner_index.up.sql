-- Admission counts one owner's live generations to bound how many run at once. Partial, so the
-- count reads only pending and running rows, whatever the settled ones awaiting the purge add up to.
CREATE INDEX generations_live_owner_idx ON generations (owner_id)
WHERE
  status IN ('pending', 'running');
