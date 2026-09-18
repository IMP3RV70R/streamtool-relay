CREATE TABLE source_slates (
 source_id uuid PRIMARY KEY REFERENCES streams(id),
 on_source_loss boolean NOT NULL DEFAULT true,
 forced boolean NOT NULL DEFAULT false,
 generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
 updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO source_slates(source_id)
SELECT id FROM streams;

ALTER TABLE worker_allocations
 ADD COLUMN fallback_forced boolean NOT NULL DEFAULT false,
 ADD COLUMN input_live boolean NOT NULL DEFAULT false,
 ADD COLUMN input_unavailable boolean NOT NULL DEFAULT false;
