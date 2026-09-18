CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TYPE session_phase AS ENUM ('PENDING_INGEST','SCHEDULING','STARTING','LIVE','STOPPING','ENDED','FAILED');
CREATE TYPE allocation_state AS ENUM ('PENDING','STARTING','RUNNING','STOPPING','STOPPED','FAILED');
CREATE TYPE destination_state AS ENUM ('DISABLED','CONNECTING','STREAMING','RECONNECTING','FAILED','STOPPED');
CREATE TYPE reservation_state AS ENUM ('HELD','COMMITTED','RELEASED','EXPIRED');

CREATE TABLE accounts(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE streams(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), account_id uuid NOT NULL REFERENCES accounts(id), name text NOT NULL,
 enabled boolean NOT NULL DEFAULT true, ingest_key_hash bytea NOT NULL, region text NOT NULL DEFAULT 'local', generation bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(account_id,name));
CREATE TABLE destinations(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), stream_id uuid NOT NULL REFERENCES streams(id) ON DELETE CASCADE, name text NOT NULL,
 enabled boolean NOT NULL DEFAULT true, endpoint text NOT NULL, secret_ciphertext bytea NOT NULL, secret_nonce bytea NOT NULL, key_id text NOT NULL,
 generation bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE ingest_connections(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), edge_id text NOT NULL, connection_id text NOT NULL, stream_id uuid REFERENCES streams(id),
 protocol text NOT NULL CHECK(protocol IN ('srt','rtmp')), status text NOT NULL CHECK(status IN ('CONNECTED','DISCONNECTED','REJECTED')),
 connected_at timestamptz NOT NULL, last_seen_at timestamptz NOT NULL, disconnected_at timestamptz, metadata jsonb NOT NULL DEFAULT '{}',
 UNIQUE(edge_id,connection_id));
CREATE TABLE stream_sessions(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), stream_id uuid NOT NULL REFERENCES streams(id), ingest_connection_id uuid NOT NULL REFERENCES ingest_connections(id),
 desired text NOT NULL DEFAULT 'RUNNING' CHECK(desired IN ('RUNNING','STOPPED')), phase session_phase NOT NULL DEFAULT 'PENDING_INGEST', generation bigint NOT NULL DEFAULT 1,
 fencing_token bigint NOT NULL DEFAULT 1, started_at timestamptz NOT NULL DEFAULT now(), ended_at timestamptz, end_reason text, updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX one_active_session_per_stream ON stream_sessions(stream_id) WHERE phase NOT IN ('ENDED','FAILED');
CREATE TABLE media_nodes(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), region text NOT NULL, endpoint text NOT NULL, state text NOT NULL CHECK(state IN ('ACTIVE','DRAINING','OFFLINE')),
 slots int NOT NULL, cpu_millis bigint NOT NULL, memory_bytes bigint NOT NULL, ingress_bps bigint NOT NULL, egress_bps bigint NOT NULL,
 last_seen_at timestamptz NOT NULL, generation bigint NOT NULL DEFAULT 1);
CREATE TABLE worker_allocations(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), session_id uuid NOT NULL REFERENCES stream_sessions(id), node_id uuid REFERENCES media_nodes(id), runtime text NOT NULL DEFAULT 'docker',
 desired text NOT NULL CHECK(desired IN ('RUNNING','STOPPED')), state allocation_state NOT NULL DEFAULT 'PENDING', desired_generation bigint NOT NULL,
 observed_generation bigint NOT NULL DEFAULT 0, fencing_token bigint NOT NULL, worker_id text, last_error text, updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX one_active_allocation_per_session ON worker_allocations(session_id) WHERE state NOT IN ('STOPPED','FAILED');
CREATE TABLE destination_runtimes(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), allocation_id uuid NOT NULL REFERENCES worker_allocations(id) ON DELETE CASCADE,
 destination_id uuid NOT NULL REFERENCES destinations(id), desired text NOT NULL, state destination_state NOT NULL, generation bigint NOT NULL,
 reconnect_count bigint NOT NULL DEFAULT 0, bytes_sent bigint NOT NULL DEFAULT 0, last_error_code text, last_error_at timestamptz, last_seen_at timestamptz NOT NULL,
 UNIQUE(allocation_id,destination_id));
CREATE TABLE reservations(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), allocation_id uuid NOT NULL UNIQUE REFERENCES worker_allocations(id), node_id uuid NOT NULL REFERENCES media_nodes(id),
 slots int NOT NULL, cpu_millis bigint NOT NULL, memory_bytes bigint NOT NULL, ingress_bps bigint NOT NULL, egress_bps bigint NOT NULL,
 state reservation_state NOT NULL, expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE leases(
 resource_type text NOT NULL, resource_id uuid NOT NULL, owner_id text NOT NULL, fencing_token bigint NOT NULL, expires_at timestamptz NOT NULL,
 PRIMARY KEY(resource_type,resource_id));
CREATE TABLE audit_records(id bigserial PRIMARY KEY, account_id uuid, action text NOT NULL, resource_type text NOT NULL, resource_id uuid, details jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX ingest_last_seen ON ingest_connections(last_seen_at); CREATE INDEX sessions_phase ON stream_sessions(phase); CREATE INDEX reservations_expiry ON reservations(expires_at) WHERE state='HELD';
