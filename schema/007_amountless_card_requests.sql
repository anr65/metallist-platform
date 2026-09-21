-- Apply after 006 in one transaction. Historical request amounts remain unchanged.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 6 THEN
        RAISE EXCEPTION 'migration 007 requires schema version 6';
    END IF;
END $$;

ALTER TABLE payment_requests DROP CONSTRAINT payment_requests_mode_check;
ALTER TABLE payment_requests ADD CONSTRAINT payment_requests_mode_check
    CHECK (mode IN ('count', 'total', 'manual', 'cards'));
ALTER TABLE payment_requests ALTER COLUMN per_payment_cents DROP NOT NULL;
ALTER TABLE payment_requests ALTER COLUMN requested_total_cents DROP NOT NULL;
ALTER TABLE payment_requests ADD CONSTRAINT payment_requests_amount_mode_check
    CHECK ((mode = 'cards' AND per_payment_cents IS NULL AND requested_total_cents IS NULL)
        OR (mode <> 'cards' AND per_payment_cents IS NOT NULL AND requested_total_cents IS NOT NULL));
ALTER TABLE payment_request_rows ALTER COLUMN planned_cents DROP NOT NULL;

INSERT INTO schema_migrations(version) VALUES (7);
