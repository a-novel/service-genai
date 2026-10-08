-- Every started attempt carried over has an epoch, and an unstarted one is left for any replica.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM generations WHERE start_requested_at IS NOT NULL AND provider_epoch IS DISTINCT FROM 1) THEN
    RAISE EXCEPTION 'started attempt without the first epoch';
  END IF;
  IF EXISTS (SELECT 1 FROM generations WHERE start_requested_at IS NULL AND provider_epoch IS NOT NULL) THEN
    RAISE EXCEPTION 'unstarted generation claimed by an epoch';
  END IF;
END;
$$;
