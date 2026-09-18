DROP TABLE node_fences;
ALTER TABLE media_nodes DROP COLUMN unreachable_since, DROP COLUMN instance_id;
ALTER TABLE stream_sessions DROP COLUMN node_recovery_count;
ALTER TABLE worker_allocations DROP COLUMN cpu_millis, DROP COLUMN memory_bytes, DROP COLUMN ingress_bps, DROP COLUMN egress_bps;
