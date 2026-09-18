-- Restore the agreed non-recoverable ingest credential policy.
-- Existing stream hashes and camera credentials remain valid.
ALTER TABLE account_cameras DROP COLUMN key_ciphertext, DROP COLUMN key_nonce, DROP COLUMN key_id;
