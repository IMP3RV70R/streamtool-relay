-- Decryption material cannot be reconstructed; existing hashes remain valid.
ALTER TABLE account_cameras ADD COLUMN key_ciphertext bytea, ADD COLUMN key_nonce bytea, ADD COLUMN key_id text;
