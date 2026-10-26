-- Additive delivery queue; apply after 029 in one transaction.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 29 THEN
  RAISE EXCEPTION 'schema 030 requires version 29';
 END IF;
END $$;
CREATE TABLE telegram_prompt_deletions (
 chat_id bigint NOT NULL CHECK(chat_id<>0),
 message_id bigint NOT NULL CHECK(message_id>0),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(chat_id,message_id)
);
CREATE INDEX telegram_prompt_deletions_due ON telegram_prompt_deletions(next_attempt_at);
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='metallist_demo_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON telegram_prompt_deletions TO metallist_demo_app;
 END IF;
END $$;
INSERT INTO schema_migrations(version) VALUES(30);
