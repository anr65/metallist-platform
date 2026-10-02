-- Apply once after schema 018 or the production version marker 019, in one transaction after the environment gate.
DO $$ BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) NOT IN (18, 19) THEN
        RAISE EXCEPTION 'schema 020 requires version 18 or 19';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM registry_parser_types WHERE code = 'sveta_cards_xls_v1' AND version = 2) THEN
        RAISE EXCEPTION 'schema 020 requires Sveta parser version 2';
    END IF;
END $$;

UPDATE registry_parser_types SET version = 3 WHERE code = 'sveta_cards_xls_v1';

INSERT INTO schema_migrations(version) VALUES (20);
