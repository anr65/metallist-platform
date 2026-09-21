-- A prepared card request may be revised until a response registry is linked.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 10 THEN
        RAISE EXCEPTION 'migration 011 requires schema version 10';
    END IF;
END $$;

ALTER TABLE payment_requests ADD COLUMN version integer NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE payment_requests ADD COLUMN deleted_at timestamptz;
ALTER TABLE payment_requests ADD COLUMN deleted_by uuid REFERENCES users(id);
ALTER TABLE payment_requests ADD CONSTRAINT payment_requests_deletion_check
    CHECK ((deleted_at IS NULL) = (deleted_by IS NULL));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        GRANT UPDATE (payment_count, version, deleted_at, deleted_by) ON payment_requests TO metallist_demo_app;
        GRANT DELETE ON payment_request_rows TO metallist_demo_app;
    END IF;
END $$;

INSERT INTO schema_migrations(version) VALUES (11);
