DO $$ BEGIN RAISE EXCEPTION 'Fallback revisions are durable; restore a backup for an incompatible rollback'; END $$;
