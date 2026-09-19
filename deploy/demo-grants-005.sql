-- Run only with schema/005_payment_contacts.sql in the same transaction,
-- after explicit authorization for metallist_demo and a verified restorable backup.
DO $$
BEGIN
    IF current_database() <> 'metallist_demo' THEN
        RAISE EXCEPTION 'unexpected database: %', current_database();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 5) THEN
        RAISE EXCEPTION 'schema version 5 is required';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        RAISE EXCEPTION 'expected application role is missing';
    END IF;
END $$;

GRANT SELECT,INSERT ON payment_contacts TO metallist_demo_app;
GRANT INSERT(contact_id,contact_name,contact_phone) ON payment_request_rows TO metallist_demo_app;
