-- Run only with schema/004_merchant_import_profiles.sql in the same transaction,
-- after explicit authorization for metallist_demo and a verified restorable backup.
DO $$
BEGIN
    IF current_database() <> 'metallist_demo' THEN
        RAISE EXCEPTION 'unexpected database: %', current_database();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 4) THEN
        RAISE EXCEPTION 'schema version 4 is required';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        RAISE EXCEPTION 'expected application role is missing';
    END IF;
END $$;

GRANT SELECT ON registry_parser_types TO metallist_demo_app;
GRANT SELECT,INSERT ON merchant_import_profiles TO metallist_demo_app;
