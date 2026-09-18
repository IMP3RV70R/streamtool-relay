ALTER TABLE worker_allocations ADD COLUMN input_error text NOT NULL DEFAULT '' CHECK (input_error IN ('','UNSUPPORTED_MEDIA'));
