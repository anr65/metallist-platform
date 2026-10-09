-- Apply once after schema 024 in one transaction after the environment gate.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 24 THEN
  RAISE EXCEPTION 'schema 025 requires version 24';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM registry_parser_types WHERE code='narkoman_avangard_xls_v1' AND version=1) THEN
  RAISE EXCEPTION 'schema 025 requires Narkoman parser version 1';
 END IF;
END $$;
UPDATE registry_parser_types SET version=2 WHERE code='narkoman_avangard_xls_v1';
INSERT INTO schema_migrations(version) VALUES (25);
