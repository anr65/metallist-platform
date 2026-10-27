-- Additive queue and immutable confirmation previews; no financial backfill.
SET LOCAL lock_timeout = '5s';
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 31 THEN
  RAISE EXCEPTION 'schema 032 requires version 31';
 END IF;
END $$;
CREATE TABLE accounting_notifications (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 event_kind text NOT NULL CHECK(event_kind IN ('repayment','handover','registry')),
 object_id uuid NOT NULL,
 details jsonb NOT NULL DEFAULT '{}'::jsonb,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sent','cancelled')),
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,event_kind,object_id)
);
CREATE INDEX accounting_notifications_pending ON accounting_notifications(next_attempt_at) WHERE state='pending';
CREATE TABLE accounting_registry_previews (
 id uuid PRIMARY KEY,
 notification_id uuid NOT NULL REFERENCES accounting_notifications(id),
 telegram_id bigint NOT NULL CHECK(telegram_id>0),
 fingerprint text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='metallist_demo_app') THEN
  GRANT SELECT,INSERT,UPDATE ON accounting_notifications TO metallist_demo_app;
  GRANT SELECT,INSERT ON accounting_registry_previews TO metallist_demo_app;
 END IF;
END $$;
INSERT INTO schema_migrations(version) VALUES(32);
