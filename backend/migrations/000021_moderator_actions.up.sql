CREATE TABLE twitch_moderator_actions(
 id bigserial PRIMARY KEY,
 channel_id uuid NOT NULL REFERENCES twitch_channels(id) ON DELETE CASCADE,
 community_event_id bigint UNIQUE REFERENCES twitch_community_events(id) ON DELETE SET NULL,
 at timestamptz NOT NULL,
 broadcast_id text NOT NULL DEFAULT '',
 assignment text NOT NULL DEFAULT 'UNASSIGNED',
 broadcaster_id text NOT NULL,
 source_broadcaster_id text NOT NULL DEFAULT '',
 moderator_id text NOT NULL,
 moderator_login text NOT NULL DEFAULT '',
 moderator_name text NOT NULL DEFAULT '',
 action text NOT NULL,
 details jsonb NOT NULL
);
CREATE INDEX twitch_moderator_actions_history ON twitch_moderator_actions(channel_id,at DESC,id DESC);

-- Older code blanked hot message text after Twitch deletion events. Restore what is
-- still available in the durable archive queue before any subsequent compaction.
UPDATE twitch_community_events e
SET message=q.payload->>'text'
FROM chat_archive_queue q
WHERE q.community_event_id=e.id
  AND e.type='channel.chat.message'
  AND e.message=''
  AND COALESCE(q.payload->>'text','')<>'';
