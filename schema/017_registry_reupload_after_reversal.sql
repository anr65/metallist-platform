-- A corrected registry may reuse the original file only after its prior registry was reversed.
DO $$ BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 16 THEN
        RAISE EXCEPTION 'schema 017 requires version 16';
    END IF;
END $$;

ALTER TABLE source_documents DROP CONSTRAINT source_documents_sha256_key;
CREATE INDEX source_documents_sha256_idx ON source_documents(sha256);
CREATE UNIQUE INDEX source_documents_manual_sha256_key ON source_documents(sha256) WHERE kind = 'manual';
ALTER TABLE registries ADD COLUMN replaces_registry_id uuid REFERENCES registries(id);

INSERT INTO schema_migrations(version) VALUES (17);
