-- Reserve one channel per account across standalone and legacy stream OAuth routes.
ALTER TABLE twitch_channels ADD CONSTRAINT twitch_channels_account_identity UNIQUE(account_id,id);
CREATE TABLE account_twitch_channels (
 account_id uuid PRIMARY KEY REFERENCES accounts(id),
 channel_id uuid NOT NULL UNIQUE,
 FOREIGN KEY(account_id,channel_id) REFERENCES twitch_channels(account_id,id)
);
-- Fail rather than silently discard an existing second channel or its history.
INSERT INTO account_twitch_channels(account_id,channel_id)
 SELECT c.account_id,c.id FROM twitch_channels c
 WHERE c.stream_id IS NULL OR EXISTS(SELECT 1 FROM twitch_connections t WHERE t.stream_id=c.id);
