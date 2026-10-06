-- Apply once after schema 023 in one transaction after the environment gate.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 23 THEN
  RAISE EXCEPTION 'schema 024 requires version 23';
 END IF;
END $$;
ALTER TABLE telegram_dialogs ADD COLUMN context jsonb NOT NULL DEFAULT '{}'::jsonb
 CHECK (jsonb_typeof(context)='object');
INSERT INTO schema_migrations(version) VALUES (24);
