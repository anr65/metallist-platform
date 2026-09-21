-- Apply after 009. Existing cards and their bank references remain unchanged.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 9 THEN
        RAISE EXCEPTION 'migration 010 requires schema version 9';
    END IF;
END $$;

ALTER TABLE banks ADD COLUMN source text NOT NULL DEFAULT 'manual';
ALTER TABLE banks ADD COLUMN external_id uuid UNIQUE;
ALTER TABLE banks ADD COLUMN selectable boolean NOT NULL DEFAULT true;
ALTER TABLE banks ADD COLUMN last_synced_at timestamptz;
ALTER TABLE banks ADD CONSTRAINT banks_source_external_id_check CHECK (
    (source = 'manual' AND external_id IS NULL) OR
    (source = 'nspk_sbp' AND external_id IS NOT NULL)
);

-- These four records belong to existing cards but are placeholders, not bank
-- identities verified against the NSPK directory.
UPDATE banks SET selectable = false
WHERE code IN ('BIN220015', 'BIN220038', 'DEMO1', 'DEMO2');

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        GRANT UPDATE (name, selectable, last_synced_at) ON banks TO metallist_demo_app;
    END IF;
END $$;

INSERT INTO schema_migrations(version) VALUES (10);
