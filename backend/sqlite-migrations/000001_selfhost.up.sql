-- SQLite baseline. PostgreSQL migration history remains in ../migrations.
CREATE TABLE accounts(id TEXT PRIMARY KEY DEFAULT (uuid()), name text NOT NULL, created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')));
CREATE TABLE streams(
 id TEXT PRIMARY KEY DEFAULT (uuid()), account_id TEXT NOT NULL REFERENCES accounts(id), name text NOT NULL,
 enabled boolean NOT NULL DEFAULT true, ingest_key_hash BLOB NOT NULL, region text NOT NULL DEFAULT 'local', generation INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')), updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')), UNIQUE(account_id,name));
CREATE TABLE destinations(
 id TEXT PRIMARY KEY DEFAULT (uuid()), stream_id TEXT NOT NULL REFERENCES streams(id) ON DELETE CASCADE, name text NOT NULL,
 enabled boolean NOT NULL DEFAULT true, endpoint text NOT NULL, secret_ciphertext BLOB NOT NULL, secret_nonce BLOB NOT NULL, key_id text NOT NULL,
 generation INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')), updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')));
CREATE TABLE ingest_connections(
 id TEXT PRIMARY KEY DEFAULT (uuid()), edge_id text NOT NULL, connection_id text NOT NULL, stream_id TEXT REFERENCES streams(id),
 protocol text NOT NULL CHECK(protocol IN ('srt','rtmp')), status text NOT NULL CHECK(status IN ('CONNECTED','DISCONNECTED','REJECTED')),
 connected_at TEXT NOT NULL, last_seen_at TEXT NOT NULL, disconnected_at TEXT, metadata TEXT NOT NULL DEFAULT '{}',
 UNIQUE(edge_id,connection_id));
CREATE TABLE stream_sessions(
 id TEXT PRIMARY KEY DEFAULT (uuid()), stream_id TEXT NOT NULL REFERENCES streams(id), ingest_connection_id TEXT NOT NULL REFERENCES ingest_connections(id),
 desired text NOT NULL DEFAULT 'RUNNING' CHECK(desired IN ('RUNNING','STOPPED')), phase TEXT NOT NULL DEFAULT 'PENDING_INGEST', generation INTEGER NOT NULL DEFAULT 1,
 fencing_token INTEGER NOT NULL DEFAULT 1, started_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')), ended_at TEXT, end_reason text, updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')));
CREATE UNIQUE INDEX one_active_session_per_stream ON stream_sessions(stream_id) WHERE phase NOT IN ('ENDED','FAILED');
CREATE TABLE media_nodes(
 id TEXT PRIMARY KEY DEFAULT (uuid()), region text NOT NULL, endpoint text NOT NULL, state text NOT NULL CHECK(state IN ('ACTIVE','DRAINING','OFFLINE')),
 slots int NOT NULL, cpu_millis INTEGER NOT NULL, memory_bytes INTEGER NOT NULL, ingress_bps INTEGER NOT NULL, egress_bps INTEGER NOT NULL,
 last_seen_at TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1);
CREATE TABLE worker_allocations(
 id TEXT PRIMARY KEY DEFAULT (uuid()), session_id TEXT NOT NULL REFERENCES stream_sessions(id), node_id TEXT REFERENCES media_nodes(id), runtime text NOT NULL DEFAULT 'docker',
 desired text NOT NULL CHECK(desired IN ('RUNNING','STOPPED')), state TEXT NOT NULL DEFAULT 'PENDING', desired_generation INTEGER NOT NULL,
 observed_generation INTEGER NOT NULL DEFAULT 0, fencing_token INTEGER NOT NULL, worker_id text, last_error text, updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')));
CREATE UNIQUE INDEX one_active_allocation_per_session ON worker_allocations(session_id) WHERE state NOT IN ('STOPPED','FAILED');
CREATE TABLE destination_runtimes(
 id TEXT PRIMARY KEY DEFAULT (uuid()), allocation_id TEXT NOT NULL REFERENCES worker_allocations(id) ON DELETE CASCADE,
 destination_id TEXT NOT NULL REFERENCES destinations(id), desired text NOT NULL, state TEXT NOT NULL, generation INTEGER NOT NULL,
 reconnect_count INTEGER NOT NULL DEFAULT 0, bytes_sent INTEGER NOT NULL DEFAULT 0, last_error_code text, last_error_at TEXT, last_seen_at TEXT NOT NULL,
 UNIQUE(allocation_id,destination_id));
CREATE TABLE reservations(
 id TEXT PRIMARY KEY DEFAULT (uuid()), allocation_id TEXT NOT NULL UNIQUE REFERENCES worker_allocations(id), node_id TEXT NOT NULL REFERENCES media_nodes(id),
 slots int NOT NULL, cpu_millis INTEGER NOT NULL, memory_bytes INTEGER NOT NULL, ingress_bps INTEGER NOT NULL, egress_bps INTEGER NOT NULL,
 state TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')));
CREATE TABLE leases(
 resource_type text NOT NULL, resource_id TEXT NOT NULL, owner_id text NOT NULL, fencing_token INTEGER NOT NULL, expires_at TEXT NOT NULL,
 PRIMARY KEY(resource_type,resource_id));
CREATE TABLE audit_records(id INTEGER PRIMARY KEY AUTOINCREMENT, account_id TEXT, action text NOT NULL, resource_type text NOT NULL, resource_id TEXT, details TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')));
CREATE INDEX ingest_last_seen ON ingest_connections(last_seen_at); CREATE INDEX sessions_phase ON stream_sessions(phase); CREATE INDEX reservations_expiry ON reservations(expires_at) WHERE state='HELD';


ALTER TABLE stream_sessions ADD COLUMN stop_after TEXT;
ALTER TABLE stream_sessions ADD COLUMN operator_stopped boolean NOT NULL DEFAULT false;
ALTER TABLE worker_allocations ADD COLUMN restart_count integer NOT NULL DEFAULT 0;
ALTER TABLE worker_allocations ADD COLUMN retry_after TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now'));
ALTER TABLE worker_allocations ADD COLUMN last_healthy_at TEXT;
ALTER TABLE worker_allocations ADD COLUMN config_hash text NOT NULL DEFAULT '';

ALTER TABLE worker_allocations ADD COLUMN fallback_active boolean NOT NULL DEFAULT false;

CREATE TABLE users(id TEXT PRIMARY KEY DEFAULT (uuid()), account_id TEXT NOT NULL UNIQUE REFERENCES accounts(id), email text NOT NULL UNIQUE, password_hash BLOB NOT NULL, password_salt BLOB NOT NULL, created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')));
CREATE TABLE user_sessions(token_hash BLOB PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at TEXT NOT NULL);
CREATE INDEX user_sessions_expiry ON user_sessions(expires_at);
CREATE TABLE auth_attempts(key text PRIMARY KEY, count int NOT NULL, expires_at TEXT NOT NULL);

ALTER TABLE destinations ADD COLUMN archived boolean NOT NULL DEFAULT false;


CREATE TABLE source_slates (
 source_id TEXT PRIMARY KEY REFERENCES streams(id),
 on_source_loss boolean NOT NULL DEFAULT true,
 forced boolean NOT NULL DEFAULT false,
 generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
 updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

INSERT INTO source_slates(source_id)
SELECT id FROM streams;

ALTER TABLE worker_allocations ADD COLUMN fallback_forced boolean NOT NULL DEFAULT false;
ALTER TABLE worker_allocations ADD COLUMN input_live boolean NOT NULL DEFAULT false;
ALTER TABLE worker_allocations ADD COLUMN input_unavailable boolean NOT NULL DEFAULT false;

ALTER TABLE worker_allocations ADD COLUMN cpu_millis INTEGER NOT NULL DEFAULT 0 CHECK(cpu_millis>=0);
ALTER TABLE worker_allocations ADD COLUMN memory_bytes INTEGER NOT NULL DEFAULT 0 CHECK(memory_bytes>=0);
ALTER TABLE worker_allocations ADD COLUMN ingress_bps INTEGER NOT NULL DEFAULT 0 CHECK(ingress_bps>=0);
ALTER TABLE worker_allocations ADD COLUMN egress_bps INTEGER NOT NULL DEFAULT 0 CHECK(egress_bps>=0);
ALTER TABLE stream_sessions ADD COLUMN node_recovery_count integer NOT NULL DEFAULT 0 CHECK(node_recovery_count>=0);
ALTER TABLE media_nodes ADD COLUMN unreachable_since TEXT;
ALTER TABLE media_nodes ADD COLUMN instance_id text NOT NULL DEFAULT '';
CREATE TABLE node_fences(
 node_id TEXT PRIMARY KEY REFERENCES media_nodes(id), instance_id text NOT NULL,
 requested_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')), confirmed_at TEXT,
 fencing_token INTEGER NOT NULL
);

CREATE TABLE edge_observation_health(edge_id text PRIMARY KEY,last_seen_at TEXT NOT NULL);

CREATE UNIQUE INDEX one_output_per_source ON destinations(stream_id) WHERE NOT archived;
CREATE TABLE source_media_profiles (
 source_id TEXT PRIMARY KEY REFERENCES streams(id),
 width integer NOT NULL DEFAULT 1280,
 height integer NOT NULL DEFAULT 720,
 fps_num integer NOT NULL DEFAULT 30,
 fps_den integer NOT NULL DEFAULT 1,
 video_kbps integer NOT NULL DEFAULT 3000 CHECK(video_kbps BETWEEN 100 AND 8000),
 audio_kbps integer NOT NULL DEFAULT 160 CHECK(audio_kbps BETWEEN 64 AND 320),
 generation INTEGER NOT NULL DEFAULT 1 CHECK(generation>0),
 CHECK(width BETWEEN 160 AND 1920 AND height BETWEEN 90 AND 1920 AND width*height<=2073600 AND width%2=0 AND height%2=0),
 CHECK(fps_num BETWEEN 1 AND 60000 AND fps_den BETWEEN 1 AND 1001 AND fps_num BETWEEN 15*fps_den AND 60*fps_den)
);
INSERT INTO source_media_profiles(source_id) SELECT id FROM streams;

CREATE TABLE account_sources(account_id TEXT PRIMARY KEY REFERENCES accounts(id),stream_id TEXT NOT NULL UNIQUE REFERENCES streams(id));
CREATE TABLE source_fallback_assets(source_id TEXT PRIMARY KEY REFERENCES streams(id),kind TEXT NOT NULL CHECK(kind IN ('default','image','video')),content BLOB NOT NULL,digest TEXT NOT NULL,generation INTEGER NOT NULL CHECK(generation>0),CHECK((kind='default' AND length(content)=0 AND digest='') OR (kind<>'default' AND length(content) BETWEEN 1 AND 52428800 AND length(digest)=64)));
ALTER TABLE worker_allocations ADD COLUMN input_error TEXT NOT NULL DEFAULT '' CHECK(input_error IN ('','UNSUPPORTED_MEDIA'));
CREATE TABLE controller_fencing(singleton INTEGER PRIMARY KEY CHECK(singleton=1),value INTEGER NOT NULL CHECK(value>=0));
INSERT INTO controller_fencing VALUES(1,0);

CREATE TABLE installation(singleton INTEGER PRIMARY KEY CHECK(singleton=1),owner_user TEXT NOT NULL UNIQUE REFERENCES users(id));
