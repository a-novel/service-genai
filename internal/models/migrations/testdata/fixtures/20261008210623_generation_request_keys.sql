-- Every generation carried over has a key.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM generations WHERE request_key IS NULL) THEN
    RAISE EXCEPTION 'generation without a request key';
  END IF;
END;
$$;
