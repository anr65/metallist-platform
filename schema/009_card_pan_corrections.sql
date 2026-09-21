-- Apply after 008 in one transaction. Existing encrypted PAN files remain valid.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 8 THEN
        RAISE EXCEPTION 'migration 009 requires schema version 8';
    END IF;
END $$;

-- Corrections are encrypted with PAN_KEY_FILE and committed atomically with the
-- new mask and audit entry. NULL continues to mean the original file vault.
ALTER TABLE cards ADD COLUMN pan_ciphertext bytea;

-- The production application role previously only inserted card records.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        GRANT UPDATE (mask,last4,pan_ciphertext) ON cards TO metallist_demo_app;
    END IF;
END $$;

INSERT INTO schema_migrations(version) VALUES (9);
