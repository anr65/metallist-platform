-- Apply once after schema 021 in one transaction after the environment gate.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 21 THEN
  RAISE EXCEPTION 'schema 022 requires version 21';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM registry_parser_types WHERE code='sveta_cards_xls_v1' AND version=4) THEN
  RAISE EXCEPTION 'schema 022 requires Sveta parser version 4';
 END IF;
END $$;
UPDATE registry_parser_types SET version=5 WHERE code='sveta_cards_xls_v1';
INSERT INTO schema_migrations(version) VALUES (22);
