ALTER TABLE twitch_community_snapshots RENAME CONSTRAINT twitch_community_snapshots_channel_id_fkey TO twitch_community_snapshots_stream_id_fkey;
ALTER TABLE twitch_community_events RENAME CONSTRAINT twitch_community_events_channel_id_fkey TO twitch_community_events_stream_id_fkey;
ALTER TABLE twitch_event_bindings RENAME CONSTRAINT twitch_event_bindings_channel_id_fkey TO twitch_event_bindings_stream_id_fkey;
ALTER TABLE twitch_samples RENAME CONSTRAINT twitch_samples_channel_id_fkey TO twitch_samples_stream_id_fkey;
ALTER TABLE twitch_connections RENAME CONSTRAINT twitch_connections_channel_id_fkey TO twitch_connections_stream_id_fkey;
ALTER TABLE twitch_oauth_flows RENAME CONSTRAINT twitch_oauth_flows_channel_id_fkey TO twitch_oauth_flows_stream_id_fkey;

ALTER TABLE twitch_community_snapshots RENAME COLUMN channel_id TO stream_id;
ALTER TABLE twitch_community_events RENAME COLUMN channel_id TO stream_id;
ALTER TABLE twitch_event_bindings RENAME COLUMN channel_id TO stream_id;
ALTER TABLE twitch_samples RENAME COLUMN channel_id TO stream_id;
ALTER TABLE twitch_connections RENAME COLUMN channel_id TO stream_id;
ALTER TABLE twitch_oauth_flows RENAME COLUMN channel_id TO stream_id;

ALTER TABLE twitch_channels RENAME CONSTRAINT twitch_channels_source_id_fkey TO twitch_channels_stream_id_fkey;
ALTER TABLE twitch_channels RENAME CONSTRAINT twitch_channels_source_id_key TO twitch_channels_stream_id_key;
ALTER TABLE twitch_channels RENAME COLUMN source_id TO stream_id;

CREATE FUNCTION legacy_twitch_channel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO twitch_channels(id,account_id,name,stream_id) VALUES(NEW.id,NEW.account_id,NEW.name,NEW.id);
 RETURN NEW;
END $$;
CREATE TRIGGER legacy_twitch_channel AFTER INSERT ON streams FOR EACH ROW EXECUTE FUNCTION legacy_twitch_channel();
