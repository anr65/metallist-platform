-- Apply after 005 to enable explicitly selected request rows.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 5 THEN
        RAISE EXCEPTION 'migration 006 requires schema version 5';
    END IF;
END $$;
ALTER TABLE payment_requests DROP CONSTRAINT payment_requests_mode_check;
ALTER TABLE payment_requests ADD CONSTRAINT payment_requests_mode_check CHECK (mode IN ('count', 'total', 'manual'));
INSERT INTO schema_migrations(version) VALUES (6);
