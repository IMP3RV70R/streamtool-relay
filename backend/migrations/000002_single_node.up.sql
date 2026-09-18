CREATE SEQUENCE controller_fencing;
ALTER TABLE stream_sessions ADD COLUMN stop_after timestamptz;
ALTER TABLE stream_sessions ADD COLUMN operator_stopped boolean NOT NULL DEFAULT false;
ALTER TABLE worker_allocations ADD COLUMN restart_count integer NOT NULL DEFAULT 0;
ALTER TABLE worker_allocations ADD COLUMN retry_after timestamptz NOT NULL DEFAULT now();
ALTER TABLE worker_allocations ADD COLUMN last_healthy_at timestamptz;
ALTER TABLE worker_allocations ADD COLUMN config_hash text NOT NULL DEFAULT '';
