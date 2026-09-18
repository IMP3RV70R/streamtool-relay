-- Serialize output creation against its source; enforce the same quota for all
-- writers, including provider callbacks and concurrent API replicas.
CREATE FUNCTION enforce_output_quota() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT NEW.archived AND (TG_OP = 'INSERT' OR OLD.archived OR OLD.stream_id <> NEW.stream_id) THEN
  PERFORM id FROM streams WHERE id=NEW.stream_id FOR UPDATE;
  IF (SELECT count(*) FROM destinations WHERE stream_id=NEW.stream_id AND NOT archived AND id<>NEW.id)>=8 THEN
   RAISE EXCEPTION 'output quota exceeded' USING ERRCODE='P0001';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER destination_quota BEFORE INSERT OR UPDATE ON destinations
 FOR EACH ROW EXECUTE FUNCTION enforce_output_quota();
CREATE INDEX auth_attempts_expiry ON auth_attempts(expires_at);
