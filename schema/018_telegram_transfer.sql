-- Enable the separate transfer dialog after confirming the exact schema version.
DO $$ BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 17 THEN
        RAISE EXCEPTION 'schema 018 requires version 17';
    END IF;
END $$;

ALTER TABLE telegram_dialogs DROP CONSTRAINT telegram_dialogs_command_check;
ALTER TABLE telegram_dialogs ADD CONSTRAINT telegram_dialogs_command_check CHECK (command IN ('withdraw','expense','transfer'));

INSERT INTO schema_migrations(version) VALUES (18);
