CREATE TABLE twitch_channels (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 account_id uuid NOT NULL REFERENCES accounts(id),
 name text NOT NULL,
 stream_id uuid UNIQUE REFERENCES streams(id) ON DELETE SET NULL,
 twitch_link_generation bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO twitch_channels(id,account_id,name,stream_id,twitch_link_generation,created_at)
 SELECT id,account_id,name,id,twitch_link_generation,created_at FROM streams;
-- Compatibility for existing stream-based routes. Standalone channels never
-- create a stream, ingest credential, destination or worker.
CREATE FUNCTION legacy_twitch_channel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO twitch_channels(id,account_id,name,stream_id) VALUES(NEW.id,NEW.account_id,NEW.name,NEW.id);
 RETURN NEW;
END $$;
CREATE TRIGGER legacy_twitch_channel AFTER INSERT ON streams FOR EACH ROW EXECUTE FUNCTION legacy_twitch_channel();
ALTER TABLE twitch_connections ALTER COLUMN destination_id DROP NOT NULL;
ALTER TABLE twitch_connections DROP CONSTRAINT twitch_connections_destination_id_fkey;
ALTER TABLE twitch_connections ADD FOREIGN KEY(destination_id) REFERENCES destinations(id) ON DELETE SET NULL;
-- Preserve IDs and encryption AAD; legacy stream_id columns now identify channels.
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['twitch_oauth_flows','twitch_connections','twitch_samples','twitch_event_bindings','twitch_community_events','twitch_community_snapshots'] LOOP
 EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I',t,t||'_stream_id_fkey');
 EXECUTE format('ALTER TABLE %I ADD FOREIGN KEY(stream_id) REFERENCES twitch_channels(id) ON DELETE CASCADE',t);
 END LOOP;
END $$;
CREATE INDEX twitch_channels_owner ON twitch_channels(account_id,created_at);
