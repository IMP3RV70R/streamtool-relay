CREATE TABLE account_cameras (
 account_id uuid PRIMARY KEY REFERENCES accounts(id),
 stream_id uuid NOT NULL UNIQUE REFERENCES streams(id),
 key_ciphertext bytea, key_nonce bytea, key_id text
);
