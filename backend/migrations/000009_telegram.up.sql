CREATE TABLE telegram_rules(
 channel_id uuid PRIMARY KEY REFERENCES twitch_channels(id) ON DELETE CASCADE,
 chat_id text NOT NULL, template text NOT NULL,
 token_ciphertext bytea NOT NULL, token_nonce bytea NOT NULL, token_key_id text NOT NULL,
 enabled boolean NOT NULL DEFAULT false, enabled_since timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE telegram_deliveries(
 id bigserial PRIMARY KEY, channel_id uuid NOT NULL REFERENCES telegram_rules(channel_id) ON DELETE CASCADE,
 broadcaster_id text NOT NULL, broadcast_id text NOT NULL, text text NOT NULL,
 status text NOT NULL CHECK(status IN ('QUEUED','SENDING','SENT','FAILED','UNCERTAIN','SKIPPED')),
 attempts int NOT NULL DEFAULT 0, due_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 message_id bigint, error_code text NOT NULL DEFAULT '', UNIQUE(channel_id,broadcaster_id,broadcast_id)
);
CREATE INDEX telegram_queue ON telegram_deliveries(due_at) WHERE status='QUEUED';
ALTER TABLE telegram_rules ADD COLUMN delete_on_end boolean NOT NULL DEFAULT true;
ALTER TABLE telegram_deliveries ADD COLUMN started_at timestamptz NOT NULL;
ALTER TABLE telegram_deliveries ADD COLUMN delete_status text NOT NULL DEFAULT 'NONE' CHECK(delete_status IN ('NONE','QUEUED','SENDING','SENT','FAILED','UNCERTAIN','SKIPPED'));
ALTER TABLE telegram_deliveries ADD COLUMN delete_due_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE telegram_deliveries ADD COLUMN sent_chat_id text;
ALTER TABLE telegram_deliveries ADD COLUMN rule_updated_at timestamptz NOT NULL;
