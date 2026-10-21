-- Apply after 027 in one transaction; preserves existing dialogs and financial history.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 27 THEN
  RAISE EXCEPTION 'schema 028 requires version 27';
 END IF;
END $$;
ALTER TABLE telegram_dialogs DROP CONSTRAINT telegram_dialogs_command_check;
ALTER TABLE telegram_dialogs ADD CONSTRAINT telegram_dialogs_command_check
 CHECK(command IN ('withdraw','expense','transfer','card_status','block_card'));
INSERT INTO schema_migrations(version) VALUES (28);
