-- Additive presentation state only; apply after 028 in one transaction.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 28 THEN
  RAISE EXCEPTION 'schema 029 requires version 28';
 END IF;
END $$;
ALTER TABLE telegram_dialogs DROP CONSTRAINT telegram_dialogs_command_check;
ALTER TABLE telegram_dialogs ADD CONSTRAINT telegram_dialogs_command_check
 CHECK(command IN ('withdraw','expense','transfer','handover','card_status','block_card'));
CREATE TABLE telegram_operation_messages (
 draft_id uuid NOT NULL REFERENCES drafts(id),
 chat_id bigint NOT NULL CHECK(chat_id <> 0),
 message_id bigint NOT NULL CHECK(message_id > 0),
 rendered_state text NOT NULL,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(draft_id,chat_id)
);
CREATE TABLE telegram_input_errors (
 user_id uuid NOT NULL REFERENCES users(id),
 chat_id bigint NOT NULL CHECK(chat_id <> 0),
 update_id bigint NOT NULL,
 operation text NOT NULL,
 code text NOT NULL,
 field text NOT NULL,
 row_number integer NOT NULL CHECK(row_number >= 0),
 message text NOT NULL,
 message_id bigint CHECK(message_id > 0),
 resolved boolean NOT NULL DEFAULT false,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(user_id,chat_id,update_id)
);
CREATE INDEX telegram_input_errors_cleanup ON telegram_input_errors(next_attempt_at) WHERE resolved;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='metallist_demo_app') THEN
  GRANT SELECT,INSERT,UPDATE ON telegram_operation_messages TO metallist_demo_app;
  GRANT SELECT,INSERT,UPDATE,DELETE ON telegram_input_errors TO metallist_demo_app;
 END IF;
END $$;
INSERT INTO schema_migrations(version) VALUES(29);
