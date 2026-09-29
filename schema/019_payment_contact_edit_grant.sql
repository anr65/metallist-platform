-- Allow the application to edit the two descriptive contact fields.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 18 THEN
        RAISE EXCEPTION 'schema 019 requires version 18';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        RAISE EXCEPTION 'expected application role is missing';
    END IF;
END $$;

GRANT UPDATE (full_name, phone) ON payment_contacts TO metallist_demo_app;

INSERT INTO schema_migrations(version) VALUES (19);
