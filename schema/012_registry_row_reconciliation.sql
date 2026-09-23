-- Apply once after schema 011, in one transaction and only after the environment gate.
DO $$
BEGIN
    IF (SELECT MAX(version) FROM schema_migrations) <> 11 THEN
        RAISE EXCEPTION 'schema 012 requires version 11';
    END IF;
END $$;

ALTER TABLE registry_rows
    ADD COLUMN source_contact_name text;

UPDATE registry_parser_types
SET version = 2,
    accepted_extensions = ARRAY['.xls', '.xlsx']
WHERE code = 'sveta_cards_xls_v1';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        GRANT UPDATE (card_id, error_code) ON registry_rows TO metallist_demo_app;
    END IF;
END $$;

INSERT INTO schema_migrations(version) VALUES (12);
