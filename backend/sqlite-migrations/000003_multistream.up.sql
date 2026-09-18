DROP INDEX one_output_per_source;
CREATE UNIQUE INDEX active_output_name ON destinations(stream_id,name) WHERE NOT archived;
CREATE TRIGGER destinations_limit_insert BEFORE INSERT ON destinations
WHEN NOT NEW.archived AND (SELECT count(*) FROM destinations WHERE stream_id=NEW.stream_id AND NOT archived)>=10
BEGIN SELECT RAISE(ABORT,'output limit reached'); END;
CREATE TRIGGER destinations_limit_update BEFORE UPDATE OF stream_id,archived ON destinations
WHEN NOT NEW.archived AND (SELECT count(*) FROM destinations WHERE stream_id=NEW.stream_id AND NOT archived AND id<>OLD.id)>=10
BEGIN SELECT RAISE(ABORT,'output limit reached'); END;
