-- Existing owner credentials/data remain intact; sessions issued without MFA
-- are revoked. Such an owner must enroll with installation token + password.
CREATE TABLE owner_mfa(user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, secret BLOB NOT NULL, last_step INTEGER NOT NULL DEFAULT -1);
CREATE TABLE owner_recovery(code_hash BLOB PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE);
CREATE TABLE auth_enrollment(singleton INTEGER PRIMARY KEY CHECK(singleton=1), token_hash BLOB NOT NULL UNIQUE, user_id TEXT REFERENCES users(id), password_hash BLOB NOT NULL, password_salt BLOB NOT NULL, secret BLOB NOT NULL, recovery_hashes BLOB NOT NULL, expires_at TEXT NOT NULL);
DELETE FROM user_sessions;
