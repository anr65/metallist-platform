-- Add a separate display title without changing historical request numbers.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 13 THEN
        RAISE EXCEPTION 'schema 014 requires version 13';
    END IF;
END $$;

ALTER TABLE payment_requests ADD COLUMN title text;
UPDATE payment_requests SET title = external_ref WHERE title IS NULL;
ALTER TABLE payment_requests ALTER COLUMN title SET NOT NULL;
ALTER TABLE payment_requests ADD CONSTRAINT payment_requests_title_check
    CHECK (length(btrim(title)) BETWEEN 1 AND 120);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        GRANT UPDATE (title) ON payment_requests TO metallist_demo_app;
    END IF;
END $$;

INSERT INTO schema_migrations(version) VALUES (14);
