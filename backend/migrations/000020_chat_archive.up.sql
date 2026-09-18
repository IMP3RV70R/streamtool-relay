ALTER TABLE twitch_community_events ADD COLUMN archived_at timestamptz;

CREATE TABLE chat_archive_queue(
 id bigserial PRIMARY KEY,
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 community_event_id bigint UNIQUE REFERENCES twitch_community_events(id) ON DELETE SET NULL,
 event_at timestamptz NOT NULL,
 received_at timestamptz NOT NULL DEFAULT now(),
 payload jsonb NOT NULL,
 lease_id uuid,
 lease_until timestamptz,
 attempts integer NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT ''
);
CREATE INDEX chat_archive_queue_claim ON chat_archive_queue(id) WHERE lease_until IS NULL;
CREATE INDEX chat_archive_queue_expired ON chat_archive_queue(lease_until) WHERE lease_until IS NOT NULL;

CREATE TABLE chat_archive_segments(
 id uuid PRIMARY KEY,
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 object_key text NOT NULL UNIQUE,
 first_queue_id bigint NOT NULL,
 last_queue_id bigint NOT NULL,
 first_event_at timestamptz NOT NULL,
 last_event_at timestamptz NOT NULL,
 record_count integer NOT NULL CHECK(record_count>0),
 size_bytes bigint NOT NULL CHECK(size_bytes>0),
 sha256 text NOT NULL,
 format text NOT NULL DEFAULT 'ndjson.zst' CHECK(format='ndjson.zst'),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(channel_id,first_queue_id,last_queue_id)
);
CREATE INDEX chat_archive_segments_time ON chat_archive_segments(channel_id,first_event_at,last_event_at);

CREATE TABLE twitch_chat_minutes(
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 bucket timestamptz NOT NULL,
 broadcast_id text NOT NULL DEFAULT '',
 messages bigint NOT NULL CHECK(messages>=0),
 PRIMARY KEY(channel_id,bucket,broadcast_id)
);
CREATE TABLE twitch_chat_minute_authors(
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 bucket timestamptz NOT NULL,
 broadcast_id text NOT NULL DEFAULT '',
 user_id text NOT NULL,
 PRIMARY KEY(channel_id,bucket,broadcast_id,user_id)
);
CREATE TABLE twitch_chat_author_days(
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 day date NOT NULL,
 broadcast_id text NOT NULL DEFAULT '',
 user_id text NOT NULL,
 login text NOT NULL DEFAULT '',
 display_name text NOT NULL DEFAULT '',
 messages bigint NOT NULL CHECK(messages>=0),
 PRIMARY KEY(channel_id,day,broadcast_id,user_id)
);

-- Compact moderation state is authoritative for reads after raw events leave the hot table.
CREATE TABLE twitch_chat_message_tombstones(
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 broadcaster_id text NOT NULL,
 message_id text NOT NULL,
 deleted_at timestamptz NOT NULL,
 PRIMARY KEY(channel_id,broadcaster_id,message_id)
);
CREATE TABLE twitch_chat_user_clears(
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 broadcaster_id text NOT NULL,
 user_id text NOT NULL,
 cleared_at timestamptz NOT NULL,
 PRIMARY KEY(channel_id,broadcaster_id,user_id)
);
CREATE TABLE twitch_chat_clears(
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 broadcaster_id text NOT NULL,
 cleared_at timestamptz NOT NULL,
 PRIMARY KEY(channel_id,broadcaster_id)
);

INSERT INTO chat_archive_queue(channel_id,community_event_id,event_at,received_at,payload)
SELECT channel_id,id,at,received_at,jsonb_build_object(
 'version',1,'type',type,'at',at,'broadcast_id',broadcast_id,'assignment',assignment,
 'broadcaster_id',broadcaster_id,'user_id',user_id,'login',login,'display_name',display_name,
 'message_id',message_id,'text',message,'deleted',deleted,'shared_chat',other_channel_id<>'' AND other_channel_id<>broadcaster_id,
 'target_user_id',user_id
) FROM twitch_community_events WHERE type LIKE 'channel.chat.%';

INSERT INTO twitch_chat_minutes(channel_id,bucket,broadcast_id,messages)
SELECT channel_id,date_trunc('minute',at),broadcast_id,count(*)
FROM twitch_community_events
WHERE type='channel.chat.message' AND (other_channel_id='' OR other_channel_id=broadcaster_id)
GROUP BY channel_id,date_trunc('minute',at),broadcast_id;

INSERT INTO twitch_chat_minute_authors(channel_id,bucket,broadcast_id,user_id)
SELECT DISTINCT channel_id,date_trunc('minute',at),broadcast_id,user_id
FROM twitch_community_events
WHERE type='channel.chat.message' AND user_id<>'' AND (other_channel_id='' OR other_channel_id=broadcaster_id);

INSERT INTO twitch_chat_author_days(channel_id,day,broadcast_id,user_id,login,display_name,messages)
SELECT channel_id,at::date,broadcast_id,user_id,(array_agg(login ORDER BY at DESC))[1],(array_agg(display_name ORDER BY at DESC))[1],count(*)
FROM twitch_community_events
WHERE type='channel.chat.message' AND user_id<>'' AND (other_channel_id='' OR other_channel_id=broadcaster_id)
GROUP BY channel_id,at::date,broadcast_id,user_id;

INSERT INTO twitch_chat_message_tombstones(channel_id,broadcaster_id,message_id,deleted_at)
SELECT channel_id,broadcaster_id,message_id,max(at) FROM twitch_community_events
WHERE type='channel.chat.message_delete' GROUP BY channel_id,broadcaster_id,message_id;
INSERT INTO twitch_chat_user_clears(channel_id,broadcaster_id,user_id,cleared_at)
SELECT channel_id,broadcaster_id,user_id,max(at) FROM twitch_community_events
WHERE type='channel.chat.clear_user_messages' GROUP BY channel_id,broadcaster_id,user_id;
INSERT INTO twitch_chat_clears(channel_id,broadcaster_id,cleared_at)
SELECT channel_id,broadcaster_id,max(at) FROM twitch_community_events
WHERE type='channel.chat.clear' GROUP BY channel_id,broadcaster_id;
