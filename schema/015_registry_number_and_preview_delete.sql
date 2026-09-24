-- Stable internal numbering and reversible visibility for unposted registries.
DO $$ BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 14 THEN
        RAISE EXCEPTION 'schema 015 requires version 14';
    END IF;
END $$;

CREATE SEQUENCE registry_number_seq;
ALTER TABLE registries ADD COLUMN number bigint;
WITH numbered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS value FROM registries
)
UPDATE registries r SET number = numbered.value FROM numbered WHERE r.id = numbered.id;
SELECT setval('registry_number_seq', GREATEST((SELECT COALESCE(MAX(number), 0) FROM registries), 1),
    (SELECT COUNT(*) > 0 FROM registries));
ALTER TABLE registries ALTER COLUMN number SET DEFAULT nextval('registry_number_seq');
ALTER TABLE registries ALTER COLUMN number SET NOT NULL;
ALTER TABLE registries ADD CONSTRAINT registries_number_unique UNIQUE(number);
ALTER SEQUENCE registry_number_seq OWNED BY registries.number;

ALTER TABLE registries ADD COLUMN deleted_at timestamptz;
ALTER TABLE registries ADD COLUMN deleted_by uuid REFERENCES users(id);
ALTER TABLE registries DROP CONSTRAINT registries_status_check;
ALTER TABLE registries ADD CONSTRAINT registries_status_check CHECK(status IN ('preview','posted','reversed','deleted'));
ALTER TABLE registries ADD CONSTRAINT registries_deleted_state_check
    CHECK ((status = 'deleted') = (deleted_at IS NOT NULL));

ALTER TABLE merchants ADD COLUMN version integer NOT NULL DEFAULT 1;

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'metallist_demo_app') THEN
        GRANT USAGE, SELECT ON SEQUENCE registry_number_seq TO metallist_demo_app;
        GRANT UPDATE (status, deleted_at, deleted_by, version) ON registries TO metallist_demo_app;
        GRANT UPDATE (code, name, active, version) ON merchants TO metallist_demo_app;
    END IF;
END $$;

INSERT INTO schema_migrations(version) VALUES (15);
