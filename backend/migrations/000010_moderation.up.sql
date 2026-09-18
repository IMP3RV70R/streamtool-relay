-- Platform-neutral identities; connection_id belongs to a provider adapter.
CREATE TABLE moderation_policies(
 platform text NOT NULL, connection_id uuid NOT NULL, account_id uuid NOT NULL REFERENCES accounts(id),
 policy jsonb NOT NULL, revision bigint NOT NULL DEFAULT 1, activated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(platform,connection_id)
);
CREATE TABLE moderation_messages(
 platform text NOT NULL, connection_id uuid NOT NULL, broadcaster_id text NOT NULL,
 message_id text NOT NULL, user_id text NOT NULL, at timestamptz NOT NULL,
 PRIMARY KEY(platform,connection_id,broadcaster_id,message_id)
);
CREATE INDEX moderation_flood ON moderation_messages(platform,connection_id,broadcaster_id,user_id,at);
CREATE INDEX moderation_message_retention ON moderation_messages(at);
CREATE TABLE moderation_actions(
 id bigserial PRIMARY KEY, platform text NOT NULL, connection_id uuid NOT NULL,
 broadcaster_id text NOT NULL, message_id text NOT NULL, user_id text NOT NULL,
 action text NOT NULL CHECK(action IN ('delete','timeout')), duration int NOT NULL,
 reason text NOT NULL, revision bigint NOT NULL, observed_at timestamptz NOT NULL,
 status text NOT NULL CHECK(status IN ('OBSERVED','QUEUED','RUNNING','DONE','FAILED','UNKNOWN','SKIPPED')),
 error_code text NOT NULL DEFAULT '', due_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(platform,connection_id,broadcaster_id,message_id),
 FOREIGN KEY(platform,connection_id) REFERENCES moderation_policies(platform,connection_id) ON DELETE CASCADE
);
CREATE INDEX moderation_queue ON moderation_actions(due_at) WHERE status='QUEUED';
