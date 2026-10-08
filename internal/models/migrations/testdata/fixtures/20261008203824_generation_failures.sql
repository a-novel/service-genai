-- Verify the conversion as well as the structural round trip.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM generations WHERE status = 'failed' AND failure IS NULL) THEN
    RAISE EXCEPTION 'failed generation has no failure kind';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM generation_usage WHERE input_tokens = 1000 AND output_tokens = 500) THEN
    RAISE EXCEPTION 'usage of a live generation was lost';
  END IF;
END;
$$;
