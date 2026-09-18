ALTER TABLE worker_allocations ADD COLUMN cpu_millis bigint NOT NULL DEFAULT 0 CHECK(cpu_millis>=0);
ALTER TABLE worker_allocations ADD COLUMN memory_bytes bigint NOT NULL DEFAULT 0 CHECK(memory_bytes>=0);
ALTER TABLE worker_allocations ADD COLUMN ingress_bps bigint NOT NULL DEFAULT 0 CHECK(ingress_bps>=0);
ALTER TABLE worker_allocations ADD COLUMN egress_bps bigint NOT NULL DEFAULT 0 CHECK(egress_bps>=0);
ALTER TABLE stream_sessions ADD COLUMN node_recovery_count integer NOT NULL DEFAULT 0 CHECK(node_recovery_count>=0);
ALTER TABLE media_nodes ADD COLUMN unreachable_since timestamptz;
ALTER TABLE media_nodes ADD COLUMN instance_id text NOT NULL DEFAULT '';
CREATE TABLE node_fences(
 node_id uuid PRIMARY KEY REFERENCES media_nodes(id), instance_id text NOT NULL,
 requested_at timestamptz NOT NULL DEFAULT now(), confirmed_at timestamptz,
 fencing_token bigint NOT NULL
);
