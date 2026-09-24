-- Factual payment day is independent of confirmation and publication times.
-- Existing rows remain NULL: their payment day cannot be inferred reliably.
DO $$ BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 15 THEN
        RAISE EXCEPTION 'schema 016 requires version 15';
    END IF;
END $$;

ALTER TABLE registries ADD COLUMN payment_date date;
CREATE INDEX registries_payment_date_idx ON registries(payment_date) WHERE status = 'posted';

INSERT INTO schema_migrations(version) VALUES (16);
