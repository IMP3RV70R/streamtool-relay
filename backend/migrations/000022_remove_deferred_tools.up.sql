-- PRE-LAUNCH product reset: analytics, chat history, moderation and Telegram
-- were explicitly removed together with their stored data. Twitch OAuth scopes
-- remain on twitch_connections for future capabilities.
DROP TABLE IF EXISTS twitch_moderator_actions;

DROP TABLE IF EXISTS chat_archive_queue;
DROP TABLE IF EXISTS chat_archive_segments;
DROP TABLE IF EXISTS twitch_chat_minutes;
DROP TABLE IF EXISTS twitch_chat_minute_authors;
DROP TABLE IF EXISTS twitch_chat_author_days;
DROP TABLE IF EXISTS twitch_chat_message_tombstones;
DROP TABLE IF EXISTS twitch_chat_user_clears;
DROP TABLE IF EXISTS twitch_chat_clears;

DROP TABLE IF EXISTS telegram_deliveries;
DROP TABLE IF EXISTS telegram_rules;

DROP TABLE IF EXISTS moderation_actions;
DROP TABLE IF EXISTS moderation_messages;
DROP TABLE IF EXISTS moderation_policies;
ALTER TABLE accounts DROP COLUMN IF EXISTS moderation_enabled;

DROP TABLE IF EXISTS twitch_event_subscriptions;
DROP TABLE IF EXISTS twitch_community_events;
DROP TABLE IF EXISTS twitch_community_snapshots;
DROP TABLE IF EXISTS twitch_event_receipts;
DROP TABLE IF EXISTS twitch_event_bindings;
DROP TABLE IF EXISTS twitch_samples;

ALTER TABLE twitch_connections DROP COLUMN IF EXISTS community_due;
ALTER TABLE twitch_connections DROP COLUMN IF EXISTS checked_at;
ALTER TABLE twitch_connections DROP COLUMN IF EXISTS broadcast_state;
ALTER TABLE twitch_connections DROP COLUMN IF EXISTS viewer_count;
ALTER TABLE twitch_connections DROP COLUMN IF EXISTS observed_title;
