-- Existing hashes/keys are preserved; hashes cannot be reversed or silently rotated.
CREATE TABLE source_credentials (
 source_id TEXT PRIMARY KEY REFERENCES streams(id) ON DELETE CASCADE,
 secret BLOB NOT NULL CHECK(length(secret)>0)
);
