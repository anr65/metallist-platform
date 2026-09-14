-- Apply once to the exact intended database in a transaction after a verified restorable backup.
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('chief','operator','collector','accountant','sysadmin','auditor'));
ALTER TABLE users ADD COLUMN custodian_id uuid UNIQUE REFERENCES custodians(id);

ALTER TABLE telegram_updates ADD COLUMN raw_update jsonb;
CREATE TABLE telegram_dialogs (
    user_id uuid PRIMARY KEY REFERENCES users(id),
    command text NOT NULL CHECK (command IN ('withdraw','expense')),
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO schema_migrations(version) VALUES (3);
