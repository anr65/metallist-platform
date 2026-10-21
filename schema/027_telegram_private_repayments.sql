-- Additive migration; apply after 026 in one transaction.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 26 THEN
  RAISE EXCEPTION 'schema 027 requires version 26';
 END IF;
END $$;
CREATE TABLE telegram_repayment_dialogs (
 user_id uuid PRIMARY KEY REFERENCES users(id),
 context jsonb NOT NULL CHECK (jsonb_typeof(context)='object'),
 last_update_id bigint NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE TABLE telegram_repayment_notifications (
 draft_id uuid PRIMARY KEY REFERENCES drafts(id),
 chat_id bigint NOT NULL CHECK (chat_id < 0),
 message text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','sent')),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text NOT NULL DEFAULT ''
);
CREATE INDEX telegram_repayment_notifications_pending ON telegram_repayment_notifications(next_attempt_at,draft_id) WHERE state='pending';
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='metallist_demo_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON telegram_repayment_dialogs TO metallist_demo_app;
  GRANT SELECT,INSERT ON telegram_repayment_notifications TO metallist_demo_app;
  GRANT UPDATE(state,attempts,next_attempt_at,last_error) ON telegram_repayment_notifications TO metallist_demo_app;
 END IF;
END $$;
INSERT INTO schema_migrations(version) VALUES (27);
