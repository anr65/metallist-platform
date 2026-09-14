-- Apply once to the exact intended database inside a transaction after a verified backup.
CREATE TABLE payment_requests (
    id uuid PRIMARY KEY,
    merchant_id uuid NOT NULL REFERENCES merchants(id),
    external_ref text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('count', 'total')),
    payment_count integer NOT NULL CHECK (payment_count BETWEEN 1 AND 500),
    per_payment_cents bigint NOT NULL CHECK (per_payment_cents > 0),
    requested_total_cents bigint NOT NULL CHECK (requested_total_cents > 0),
    export_path text NOT NULL,
    export_sha256 text NOT NULL,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, external_ref)
);

CREATE TABLE payment_request_rows (
    id uuid PRIMARY KEY,
    request_id uuid NOT NULL REFERENCES payment_requests(id),
    row_no integer NOT NULL CHECK (row_no > 0),
    card_id uuid NOT NULL REFERENCES cards(id),
    planned_cents bigint NOT NULL CHECK (planned_cents > 0),
    synthetic_number char(16) NOT NULL CHECK (synthetic_number ~ '^[0-9]{16}$'),
    synthetic_name text NOT NULL,
    UNIQUE (request_id, row_no)
);

ALTER TABLE registries ADD COLUMN payment_request_id uuid REFERENCES payment_requests(id);
CREATE INDEX registries_payment_request_id_idx ON registries(payment_request_id) WHERE payment_request_id IS NOT NULL;
CREATE INDEX payment_request_rows_card_idx ON payment_request_rows(request_id, card_id);

INSERT INTO schema_migrations(version) VALUES (2);
