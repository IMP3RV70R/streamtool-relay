ALTER TABLE accounts ADD COLUMN balance_kopecks bigint NOT NULL DEFAULT 0;
CREATE TABLE broadcast_billing (
 session_id uuid PRIMARY KEY REFERENCES stream_sessions(id),
 account_id uuid NOT NULL REFERENCES accounts(id),
 hourly_kopecks bigint NOT NULL DEFAULT 10000 CHECK(hourly_kopecks=10000),
 elapsed_seconds numeric NOT NULL DEFAULT 0 CHECK(elapsed_seconds>=0),
 running_since timestamptz,
 charged_hours bigint NOT NULL DEFAULT 0 CHECK(charged_hours>=0),
 finalized boolean NOT NULL DEFAULT false
);
CREATE INDEX billing_open_accounts ON broadcast_billing(account_id) WHERE NOT finalized;
CREATE TABLE balance_entries (
 id bigserial PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES accounts(id),
 session_id uuid REFERENCES stream_sessions(id),
 amount_kopecks bigint NOT NULL CHECK(amount_kopecks<>0),
 kind text NOT NULL CHECK(kind IN ('BROADCAST','TEST_CREDIT')),
 request_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK((kind='TEST_CREDIT' AND request_id IS NOT NULL AND session_id IS NULL AND amount_kopecks>0)
    OR (kind='BROADCAST' AND request_id IS NULL AND session_id IS NOT NULL AND amount_kopecks<0)),
 UNIQUE(account_id,request_id)
);
CREATE FUNCTION billable_hours(seconds numeric) RETURNS bigint LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT floor(greatest(seconds,0)/3600)::bigint
  + CASE WHEN mod(greatest(seconds,0),3600)>600 THEN 1 ELSE 0 END
$$;
-- All writers lock account before billing rows. Reading session state never locks
-- session rows, so admission/reconciliation do not invert their lock ordering.
CREATE FUNCTION settle_broadcast_balance(owner uuid) RETURNS void LANGUAGE plpgsql AS $$
DECLARE b record; hours bigint; amount bigint;
BEGIN
 PERFORM id FROM accounts WHERE id=owner FOR UPDATE;
 FOR b IN SELECT * FROM broadcast_billing WHERE account_id=owner AND NOT finalized ORDER BY session_id FOR UPDATE LOOP
  hours := billable_hours(b.elapsed_seconds + CASE WHEN b.running_since IS NULL THEN 0
    ELSE greatest(extract(epoch FROM (statement_timestamp()-b.running_since)),0) END);
  IF hours>b.charged_hours THEN
   amount := (hours-b.charged_hours)*b.hourly_kopecks;
   INSERT INTO balance_entries(account_id,session_id,amount_kopecks,kind) VALUES(owner,b.session_id,-amount,'BROADCAST');
   UPDATE accounts SET balance_kopecks=balance_kopecks-amount WHERE id=owner;
   UPDATE broadcast_billing SET charged_hours=hours WHERE session_id=b.session_id;
  END IF;
 END LOOP;
END $$;
CREATE FUNCTION track_broadcast_billing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner uuid; clock timestamptz := statement_timestamp();
BEGIN
 SELECT account_id INTO owner FROM streams WHERE id=NEW.stream_id;
 PERFORM id FROM accounts WHERE id=owner FOR UPDATE;
 IF TG_OP='INSERT' THEN
  INSERT INTO broadcast_billing(session_id,account_id,running_since)
   VALUES(NEW.id,owner,CASE WHEN NEW.phase='LIVE' THEN clock END);
 ELSE
  IF NEW.phase IS NOT DISTINCT FROM OLD.phase THEN RETURN NEW; END IF;
  -- Source loss and forced slate leave phase LIVE. Recovery/scheduling pauses
  -- the billing clock; reconnects and replacement workers retain one counter.
  IF NEW.phase NOT IN ('LIVE','STOPPING') THEN
   UPDATE broadcast_billing SET elapsed_seconds=elapsed_seconds+CASE WHEN running_since IS NULL THEN 0
     ELSE greatest(extract(epoch FROM (clock-running_since)),0) END,running_since=NULL WHERE session_id=NEW.id;
  ELSIF NEW.phase='LIVE' THEN
   UPDATE broadcast_billing SET running_since=COALESCE(running_since,clock) WHERE session_id=NEW.id;
  END IF;
 END IF;
 PERFORM settle_broadcast_balance(owner);
 IF NEW.phase IN ('ENDED','FAILED') THEN UPDATE broadcast_billing SET finalized=true WHERE session_id=NEW.id; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER session_billing AFTER INSERT OR UPDATE OF phase ON stream_sessions FOR EACH ROW EXECUTE FUNCTION track_broadcast_billing();
-- Existing pre-launch sessions are not retroactively billed.
INSERT INTO broadcast_billing(session_id,account_id,running_since,finalized)
 SELECT ss.id,s.account_id,CASE WHEN ss.phase='LIVE' THEN statement_timestamp() END,ss.phase IN ('ENDED','FAILED')
 FROM stream_sessions ss JOIN streams s ON s.id=ss.stream_id;
CREATE OR REPLACE FUNCTION enforce_output_quota() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT NEW.archived AND (TG_OP = 'INSERT' OR OLD.archived OR OLD.stream_id <> NEW.stream_id) THEN
  PERFORM id FROM streams WHERE id=NEW.stream_id FOR UPDATE;
  IF (SELECT count(*) FROM destinations WHERE stream_id=NEW.stream_id AND NOT archived AND id<>NEW.id)>=10 THEN
   RAISE EXCEPTION 'output quota exceeded' USING ERRCODE='P0001';
  END IF;
 END IF;
 RETURN NEW;
END $$;
