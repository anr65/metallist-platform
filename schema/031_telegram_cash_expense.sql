-- Additive command allowlist expansion; apply after 030 in one transaction.
SET LOCAL lock_timeout = '5s';
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 30 THEN
  RAISE EXCEPTION 'schema 031 requires version 30';
 END IF;
END $$;
ALTER TABLE telegram_dialogs DROP CONSTRAINT telegram_dialogs_command_check;
ALTER TABLE telegram_dialogs ADD CONSTRAINT telegram_dialogs_command_check
 CHECK(command IN ('withdraw','expense','transfer','card_status','block_card','handover','cashexpense'));
INSERT INTO schema_migrations(version) VALUES (31);
