-- Apply after 007 in one transaction. Existing requests and files remain unchanged.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 7 THEN
        RAISE EXCEPTION 'migration 008 requires schema version 7';
    END IF;
END $$;

-- New requests stream a fresh export after decrypting the selected cards.
-- No full PAN or plaintext XLSX is persisted in PostgreSQL or DOCUMENT_DIR.
ALTER TABLE payment_requests ALTER COLUMN export_path DROP NOT NULL;
ALTER TABLE payment_requests ALTER COLUMN export_sha256 DROP NOT NULL;
ALTER TABLE payment_request_rows ALTER COLUMN synthetic_number DROP NOT NULL;
ALTER TABLE payment_request_rows ALTER COLUMN synthetic_name DROP NOT NULL;

INSERT INTO schema_migrations(version) VALUES (8);
