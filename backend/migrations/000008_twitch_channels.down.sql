DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM twitch_channels WHERE stream_id IS NULL) THEN
 RAISE EXCEPTION 'Cannot downgrade: standalone Twitch channels would lose their history';
 END IF;
END $$;
UPDATE streams s SET twitch_link_generation=c.twitch_link_generation FROM twitch_channels c WHERE c.id=s.id;
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['twitch_oauth_flows','twitch_connections','twitch_samples','twitch_event_bindings','twitch_community_events','twitch_community_snapshots'] LOOP
 EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I',t,t||'_stream_id_fkey');
 EXECUTE format('ALTER TABLE %I ADD FOREIGN KEY(stream_id) REFERENCES streams(id) ON DELETE CASCADE',t);
 END LOOP;
END $$;
ALTER TABLE twitch_connections DROP CONSTRAINT twitch_connections_destination_id_fkey;
ALTER TABLE twitch_connections ADD FOREIGN KEY(destination_id) REFERENCES destinations(id);
ALTER TABLE twitch_connections ALTER COLUMN destination_id SET NOT NULL;
DROP TRIGGER legacy_twitch_channel ON streams;
DROP FUNCTION legacy_twitch_channel();
DROP TABLE twitch_channels;
