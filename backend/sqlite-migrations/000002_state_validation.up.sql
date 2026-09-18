-- SQLite does not have PostgreSQL enum types: enforce the same durable domains.
CREATE TRIGGER sessions_validate_insert BEFORE INSERT ON stream_sessions WHEN
 NEW.phase NOT IN ('PENDING_INGEST','SCHEDULING','STARTING','LIVE','STOPPING','ENDED','FAILED') OR NEW.generation<1 OR NEW.fencing_token<1 OR NEW.operator_stopped NOT IN (0,1)
 BEGIN SELECT RAISE(ABORT,'invalid session state'); END;
CREATE TRIGGER sessions_validate_update BEFORE UPDATE ON stream_sessions WHEN
 NEW.phase NOT IN ('PENDING_INGEST','SCHEDULING','STARTING','LIVE','STOPPING','ENDED','FAILED') OR NEW.generation<1 OR NEW.fencing_token<1 OR NEW.operator_stopped NOT IN (0,1)
 BEGIN SELECT RAISE(ABORT,'invalid session state'); END;
CREATE TRIGGER allocations_validate_insert BEFORE INSERT ON worker_allocations WHEN
 NEW.state NOT IN ('PENDING','STARTING','RUNNING','STOPPING','STOPPED','FAILED') OR NEW.desired_generation<1 OR NEW.fencing_token<1 OR NEW.restart_count<0
 BEGIN SELECT RAISE(ABORT,'invalid allocation state'); END;
CREATE TRIGGER allocations_validate_update BEFORE UPDATE ON worker_allocations WHEN
 NEW.state NOT IN ('PENDING','STARTING','RUNNING','STOPPING','STOPPED','FAILED') OR NEW.desired_generation<1 OR NEW.fencing_token<1 OR NEW.restart_count<0
 BEGIN SELECT RAISE(ABORT,'invalid allocation state'); END;
CREATE TRIGGER destinations_validate_insert BEFORE INSERT ON destinations WHEN NEW.enabled NOT IN (0,1) OR NEW.archived NOT IN (0,1) OR NEW.generation<1
 BEGIN SELECT RAISE(ABORT,'invalid destination state'); END;
CREATE TRIGGER destinations_validate_update BEFORE UPDATE ON destinations WHEN NEW.enabled NOT IN (0,1) OR NEW.archived NOT IN (0,1) OR NEW.generation<1
 BEGIN SELECT RAISE(ABORT,'invalid destination state'); END;
