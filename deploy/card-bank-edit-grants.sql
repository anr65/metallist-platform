-- Additive permission change. Run in one transaction on the verified target.
-- No schema/data changes; compatible with the previous application version.
DO $$ BEGIN
 IF current_database() NOT IN ('metallist_demo', 'metallist_platform_test') THEN
  RAISE EXCEPTION 'unexpected database: %', current_database();
 END IF;
 IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version=26) THEN
  RAISE EXCEPTION 'schema 026 is required';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='metallist_demo_app') THEN
  RAISE EXCEPTION 'expected application role is missing';
 END IF;
END $$;
GRANT UPDATE(bank_id) ON cards TO metallist_demo_app;
