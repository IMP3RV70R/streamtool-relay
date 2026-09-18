CREATE TABLE users(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), account_id uuid NOT NULL UNIQUE REFERENCES accounts(id), email text NOT NULL UNIQUE, password_hash bytea NOT NULL, password_salt bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE user_sessions(token_hash bytea PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at timestamptz NOT NULL);
CREATE INDEX user_sessions_expiry ON user_sessions(expires_at);
CREATE TABLE auth_attempts(key text PRIMARY KEY, count int NOT NULL, expires_at timestamptz NOT NULL);
