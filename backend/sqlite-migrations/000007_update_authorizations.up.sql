-- Durable update-only authorization. No password, code or reusable MFA grant.
CREATE TABLE update_authorizations(
 request_id TEXT PRIMARY KEY CHECK(length(request_id)=36),
 release_digest TEXT NOT NULL CHECK(length(release_digest)=64),
 user_id TEXT NOT NULL REFERENCES users(id),
 session_hash BLOB NOT NULL CHECK(length(session_hash)=32),
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 expires_at TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','DISPATCHED','CANCELLED'))
);
CREATE UNIQUE INDEX update_authorizations_one_pending ON update_authorizations(state) WHERE state='PENDING';
