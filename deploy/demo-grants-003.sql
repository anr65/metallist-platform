-- Run only with schema/003_telegram_collectors.sql in the same transaction,
-- after explicit authorization for metallist_demo and a verified restorable backup.
DO $$
BEGIN
    IF current_database() <> 'metallist_demo' THEN
        RAISE EXCEPTION 'unexpected database: %', current_database();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 3) THEN
        RAISE EXCEPTION 'schema version 3 is required';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        RAISE EXCEPTION 'expected application role is missing';
    END IF;
END $$;

GRANT UPDATE(password_hash,telegram_id,custodian_id) ON users TO metallist_demo_app;
GRANT UPDATE(status,payload,confirmed_by,confirmed_at,confirmed_amount_cents) ON drafts TO metallist_demo_app;
GRANT UPDATE(status,commission_cents,rate_bp,confirmed_at,manual_rate_bp,manual_reason,manual_approved_by,manual_approved_at,version) ON registries TO metallist_demo_app;
GRANT UPDATE(active) ON tariffs TO metallist_demo_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON telegram_dialogs TO metallist_demo_app;
