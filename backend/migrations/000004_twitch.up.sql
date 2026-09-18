ALTER TABLE streams ADD COLUMN twitch_link_generation bigint NOT NULL DEFAULT 0;
CREATE TABLE twitch_oauth_flows (
 ticket_hash text PRIMARY KEY, state_hash text UNIQUE, browser_hash text,
 stream_id uuid NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
 generation bigint NOT NULL, expires_at timestamptz NOT NULL
);
CREATE INDEX twitch_oauth_expiry ON twitch_oauth_flows(expires_at);
CREATE TABLE twitch_connections (
 stream_id uuid PRIMARY KEY REFERENCES streams(id) ON DELETE CASCADE,
 destination_id uuid NOT NULL UNIQUE REFERENCES destinations(id),
 broadcaster_id text UNIQUE, login text NOT NULL DEFAULT '',
 state text NOT NULL CHECK(state IN ('CONNECTED','DISCONNECTED','REAUTH_REQUIRED')),
 token_ciphertext bytea, token_nonce bytea, token_key_id text,
 desired_title text NOT NULL DEFAULT '', applied_title text NOT NULL DEFAULT '',
 validated_at timestamptz, checked_at timestamptz,
 broadcast_state text NOT NULL DEFAULT 'UNKNOWN', viewer_count integer NOT NULL DEFAULT 0,
 observed_title text NOT NULL DEFAULT '', last_error text NOT NULL DEFAULT '',
 next_sync_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
