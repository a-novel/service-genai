-- Verify data conversion as well as the structural migration roundtrip.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM generations WHERE status = 'running' AND claim_token IS NULL) THEN
    RAISE EXCEPTION 'legacy running claim has no token';
  END IF;
  IF EXISTS (SELECT 1 FROM generations WHERE status IN ('running', 'pending') AND attempt > 0 AND start_requested_at IS NULL) THEN
    RAISE EXCEPTION 'legacy execution uncertainty was lost';
  END IF;
  IF EXISTS (SELECT 1 FROM generations WHERE attempt = 0 AND start_requested_at IS NOT NULL) THEN
    RAISE EXCEPTION 'unstarted generation was marked as paid work';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM generation_usage WHERE input_tokens = 1000 AND output_tokens = 500) THEN
    RAISE EXCEPTION 'usage accounting was lost';
  END IF;
END;
$$;
