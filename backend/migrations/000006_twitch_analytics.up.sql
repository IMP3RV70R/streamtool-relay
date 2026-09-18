CREATE TABLE twitch_samples (
 stream_id uuid NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
 observed_at timestamptz NOT NULL,
 bucket timestamptz NOT NULL,
 broadcaster_id text NOT NULL,
 state text NOT NULL CHECK(state IN ('LIVE','OFFLINE','UNKNOWN')),
 broadcast_id text NOT NULL DEFAULT '',
 started_at timestamptz,
 viewer_count integer CHECK(viewer_count>=0),
 title text NOT NULL DEFAULT '',
 game_id text NOT NULL DEFAULT '',
 game_name text NOT NULL DEFAULT '',
 language text NOT NULL DEFAULT '',
 error_code text NOT NULL DEFAULT '',
 PRIMARY KEY(stream_id,bucket,state,broadcast_id,broadcaster_id),
 CHECK(state <> 'LIVE' OR (broadcast_id<>'' AND started_at IS NOT NULL AND viewer_count IS NOT NULL))
);
CREATE INDEX twitch_samples_broadcast ON twitch_samples(stream_id,broadcast_id,observed_at);
