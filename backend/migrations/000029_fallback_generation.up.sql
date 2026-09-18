-- Keep a tombstone revision when returning to the built-in fallback. A stale
-- upload/delete must never target a subsequently recreated asset (ABA).
ALTER TABLE source_fallback_assets DROP CONSTRAINT source_fallback_assets_kind_check;
ALTER TABLE source_fallback_assets DROP CONSTRAINT source_fallback_assets_content_check;
ALTER TABLE source_fallback_assets DROP CONSTRAINT source_fallback_assets_digest_check;
ALTER TABLE source_fallback_assets ADD CHECK(kind IN ('default','image','video'));
ALTER TABLE source_fallback_assets ADD CHECK((kind='default' AND octet_length(content)=0 AND digest='') OR (kind IN ('image','video') AND octet_length(content) BETWEEN 1 AND 52428800 AND length(digest)=64));
