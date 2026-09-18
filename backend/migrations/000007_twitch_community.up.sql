ALTER TABLE twitch_connections ADD COLUMN granted_scopes text[] NOT NULL DEFAULT '{}';
ALTER TABLE twitch_connections ADD COLUMN community_due timestamptz NOT NULL DEFAULT now();
CREATE TABLE twitch_event_bindings(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), stream_id uuid NOT NULL REFERENCES streams(id) ON DELETE CASCADE, broadcaster_id text NOT NULL, active boolean NOT NULL DEFAULT true, created_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX twitch_active_binding ON twitch_event_bindings(stream_id) WHERE active;
INSERT INTO twitch_event_bindings(stream_id,broadcaster_id) SELECT stream_id,broadcaster_id FROM twitch_connections WHERE state='CONNECTED';
CREATE TABLE twitch_event_subscriptions(binding_id uuid NOT NULL REFERENCES twitch_event_bindings(id) ON DELETE CASCADE, type text NOT NULL, remote_id text, status text NOT NULL DEFAULT 'PENDING', error_code text NOT NULL DEFAULT '', checked_at timestamptz, last_event_at timestamptz, PRIMARY KEY(binding_id,type));
CREATE TABLE twitch_event_receipts(message_id text PRIMARY KEY, received_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE twitch_community_events(
 id bigserial PRIMARY KEY, stream_id uuid NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
 binding_id uuid NOT NULL REFERENCES twitch_event_bindings(id), broadcaster_id text NOT NULL,
 type text NOT NULL, event_key text NOT NULL, at timestamptz NOT NULL, received_at timestamptz NOT NULL DEFAULT now(),
 broadcast_id text NOT NULL DEFAULT '', assignment text NOT NULL DEFAULT 'UNASSIGNED',
 user_id text NOT NULL DEFAULT '', login text NOT NULL DEFAULT '', display_name text NOT NULL DEFAULT '',
 message_id text NOT NULL DEFAULT '', message text NOT NULL DEFAULT '', deleted boolean NOT NULL DEFAULT false,
 tier text NOT NULL DEFAULT '', is_gift boolean NOT NULL DEFAULT false, is_anonymous boolean NOT NULL DEFAULT false,
 amount bigint NOT NULL DEFAULT 0 CHECK(amount>=0), cumulative_months int NOT NULL DEFAULT 0,
 other_channel_id text NOT NULL DEFAULT '', UNIQUE(binding_id,type,event_key)
);
CREATE INDEX twitch_events_time ON twitch_community_events(stream_id,at DESC);
CREATE INDEX twitch_events_message ON twitch_community_events(stream_id,message_id) WHERE message_id<>'';
CREATE TABLE twitch_community_snapshots(
 id bigserial PRIMARY KEY, stream_id uuid NOT NULL REFERENCES streams(id) ON DELETE CASCADE, broadcaster_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('chatters','followers','subscribers')),
 observed_at timestamptz NOT NULL DEFAULT now(), total bigint, complete boolean NOT NULL DEFAULT false,
 error_code text NOT NULL DEFAULT '', members jsonb NOT NULL DEFAULT '[]', CHECK(total>=0)
);
CREATE INDEX twitch_community_latest ON twitch_community_snapshots(stream_id,kind,observed_at DESC);
CREATE INDEX twitch_receipts_retention ON twitch_event_receipts(received_at);
CREATE INDEX twitch_events_retention ON twitch_community_events(received_at);
CREATE INDEX twitch_snapshots_retention ON twitch_community_snapshots(observed_at);
