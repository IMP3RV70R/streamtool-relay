ALTER TABLE worker_allocations DROP COLUMN config_hash, DROP COLUMN last_healthy_at, DROP COLUMN retry_after, DROP COLUMN restart_count;
ALTER TABLE stream_sessions DROP COLUMN operator_stopped, DROP COLUMN stop_after;
DROP SEQUENCE controller_fencing;
