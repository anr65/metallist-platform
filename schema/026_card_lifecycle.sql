-- Additive migration; apply after 025 in one transaction.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 25 THEN
  RAISE EXCEPTION 'schema 026 requires version 25';
 END IF;
END $$;
CREATE TABLE drop_handlers (
 id uuid PRIMARY KEY, name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
 active boolean NOT NULL DEFAULT true
);
INSERT INTO drop_handlers(id,name,active) SELECT id,name,active FROM custodians WHERE kind='collector';
ALTER TABLE cards ADD COLUMN drop_handler_id uuid REFERENCES drop_handlers(id);
ALTER TABLE cards ADD COLUMN limit_until timestamptz;
ALTER TABLE cards DROP CONSTRAINT cards_status_check;
ALTER TABLE cards ADD CONSTRAINT cards_status_check CHECK(status IN ('active','limit','blocked','retired'));
ALTER TABLE cards ADD CONSTRAINT cards_limit_until_check CHECK((status='limit') = (limit_until IS NOT NULL));
CREATE INDEX cards_limit_until_idx ON cards(limit_until) WHERE status='limit';
CREATE INDEX cards_drop_handler_idx ON cards(drop_handler_id);
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='metallist_demo_app') THEN
  GRANT SELECT,INSERT ON drop_handlers TO metallist_demo_app;
  GRANT UPDATE(name,active) ON drop_handlers TO metallist_demo_app;
  GRANT UPDATE(owner_label,drop_handler_id,status,limit_until) ON cards TO metallist_demo_app;
 END IF;
END $$;
INSERT INTO schema_migrations(version) VALUES (26);
