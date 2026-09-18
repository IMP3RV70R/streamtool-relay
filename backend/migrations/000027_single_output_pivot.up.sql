-- Explicit pre-launch reset authorized by the product pivot. No legacy integration
-- or destination configuration is retained. Stop workers through normal recovery;
-- do not erase session/billing history or rotate source/account credentials.
UPDATE stream_sessions SET desired='STOPPED',operator_stopped=true,
 phase='STOPPING',generation=generation+1,updated_at=now()
 WHERE phase NOT IN ('ENDED','FAILED');
DROP TABLE account_twitch_channels;
DROP TABLE twitch_oauth_flows;
DROP TABLE twitch_connections;
DROP TABLE twitch_channels;
ALTER TABLE streams DROP COLUMN twitch_link_generation, DROP COLUMN broadcast_title;
DELETE FROM destination_runtimes;
DELETE FROM destinations;
DROP TRIGGER destination_quota ON destinations;
DROP FUNCTION enforce_output_quota();
CREATE UNIQUE INDEX one_output_per_source ON destinations(stream_id) WHERE NOT archived;
CREATE TABLE source_media_profiles (
 source_id uuid PRIMARY KEY REFERENCES streams(id),
 width integer NOT NULL DEFAULT 1280,
 height integer NOT NULL DEFAULT 720,
 fps_num integer NOT NULL DEFAULT 30,
 fps_den integer NOT NULL DEFAULT 1,
 video_kbps integer NOT NULL DEFAULT 3000 CHECK(video_kbps BETWEEN 100 AND 8000),
 audio_kbps integer NOT NULL DEFAULT 160 CHECK(audio_kbps BETWEEN 64 AND 320),
 generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 CHECK(width BETWEEN 160 AND 1920 AND height BETWEEN 90 AND 1920 AND width*height<=2073600 AND width%2=0 AND height%2=0),
 CHECK(fps_num BETWEEN 1 AND 60000 AND fps_den BETWEEN 1 AND 1001 AND fps_num BETWEEN 15*fps_den AND 60*fps_den)
);
INSERT INTO source_media_profiles(source_id) SELECT id FROM streams;
