ALTER TABLE destinations ADD COLUMN archived boolean NOT NULL DEFAULT false;
ALTER TABLE destinations ADD CONSTRAINT archived_output_disabled CHECK (NOT archived OR NOT enabled);
