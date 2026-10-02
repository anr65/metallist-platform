-- Apply once after schema 020 in one transaction after the environment gate.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 20 THEN
  RAISE EXCEPTION 'schema 021 requires version 20';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM registry_parser_types WHERE code='sveta_cards_xls_v1' AND version=3) THEN
  RAISE EXCEPTION 'schema 021 requires Sveta parser version 3';
 END IF;
END $$;
UPDATE registry_parser_types SET version=4 WHERE code='sveta_cards_xls_v1';
INSERT INTO schema_migrations(version) VALUES (21);
