-- Apply once after schema 004, in one transaction and only after the environment gate.
DO $$
BEGIN
    IF (SELECT MAX(version) FROM schema_migrations) <> 4 THEN
        RAISE EXCEPTION 'schema 005 requires version 4';
    END IF;
END $$;

CREATE TABLE payment_contacts (
    id uuid PRIMARY KEY,
    full_name text NOT NULL CHECK (length(trim(full_name)) BETWEEN 5 AND 200),
    phone text NOT NULL CHECK (phone ~ '^\+[1-9][0-9]{10,14}$'),
    active boolean NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX payment_contacts_identity_uidx
    ON payment_contacts(lower(full_name), phone);

ALTER TABLE payment_request_rows
    ADD COLUMN contact_id uuid REFERENCES payment_contacts(id),
    ADD COLUMN contact_name text,
    ADD COLUMN contact_phone text,
    ADD CONSTRAINT payment_request_rows_contact_snapshot_check CHECK (
        (contact_id IS NULL AND contact_name IS NULL AND contact_phone IS NULL)
        OR
        (contact_id IS NOT NULL AND length(trim(contact_name)) BETWEEN 5 AND 200 AND contact_phone ~ '^\+[1-9][0-9]{10,14}$')
    );

CREATE INDEX payment_request_rows_contact_idx
    ON payment_request_rows(request_id, contact_id)
    WHERE contact_id IS NOT NULL;

INSERT INTO schema_migrations(version) VALUES (5);
