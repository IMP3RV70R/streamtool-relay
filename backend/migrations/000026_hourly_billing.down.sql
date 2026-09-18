-- Monetary records must not be destroyed by an automatic rollback.
DO $$ BEGIN RAISE EXCEPTION 'billing rollback requires an explicit data-preserving migration'; END $$;
