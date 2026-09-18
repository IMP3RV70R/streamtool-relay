-- Lower the configuration quota without deleting existing destinations or keys.
DROP TRIGGER destinations_limit_insert;
DROP TRIGGER destinations_limit_update;
CREATE TRIGGER destinations_limit_insert BEFORE INSERT ON destinations
WHEN NOT NEW.archived AND (SELECT count(*) FROM destinations WHERE stream_id=NEW.stream_id AND NOT archived)>=8
BEGIN SELECT RAISE(ABORT,'output limit reached'); END;
CREATE TRIGGER destinations_limit_update BEFORE UPDATE OF stream_id,archived ON destinations
WHEN NOT NEW.archived AND (SELECT count(*) FROM destinations WHERE stream_id=NEW.stream_id AND NOT archived AND id<>OLD.id)>=8
BEGIN SELECT RAISE(ABORT,'output limit reached'); END;
