-- Removed pre-launch data cannot be recovered by a down migration. Restore a
-- pre-pivot backup to run the previous product; do not recreate empty legacy tables.
DO $$ BEGIN RAISE EXCEPTION 'single-output pivot is irreversible; restore a pre-pivot backup'; END $$;
