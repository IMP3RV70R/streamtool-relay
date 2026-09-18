CREATE TABLE source_fallback_assets (
 source_id uuid PRIMARY KEY REFERENCES streams(id),
 kind text NOT NULL CHECK(kind IN ('image','video')),
 content bytea NOT NULL CHECK(octet_length(content) BETWEEN 1 AND 52428800),
 digest text NOT NULL CHECK(length(digest)=64),
 generation bigint NOT NULL CHECK(generation>0)
);
