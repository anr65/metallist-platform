-- Apply after schema 022, in one transaction with the application stopped.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 22 THEN
  RAISE EXCEPTION 'schema 023 requires version 22';
 END IF;
END $$;

CREATE TABLE cash_reservations (
 draft_id uuid PRIMARY KEY REFERENCES drafts(id),
 custodian_id uuid NOT NULL REFERENCES custodians(id),
 amount_cents bigint NOT NULL CHECK (amount_cents > 0),
 created_at timestamptz NOT NULL DEFAULT now(),
 released_at timestamptz
);
CREATE INDEX cash_reservations_active ON cash_reservations(custodian_id) WHERE released_at IS NULL;
CREATE TABLE telegram_private_chats (
 user_id uuid PRIMARY KEY REFERENCES users(id),
 telegram_id bigint NOT NULL UNIQUE CHECK (telegram_id > 0),
 chat_id bigint NOT NULL CHECK (chat_id = telegram_id),
 started_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE transfer_notifications (
 id uuid PRIMARY KEY,
 draft_id uuid NOT NULL REFERENCES drafts(id),
 purpose text NOT NULL CHECK (purpose IN ('recipient_request','group_waiting','group_final','recipient_final')),
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','sent','cancelled')),
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_error text NOT NULL DEFAULT '',
 UNIQUE(draft_id,purpose)
);
CREATE INDEX transfer_notifications_pending ON transfer_notifications(next_attempt_at) WHERE state='pending';

-- Historical posted entries are untouched. Existing Telegram drafts require a
-- fresh sender confirmation and reservation, never an automatic reservation.
UPDATE drafts d SET payload=d.payload || jsonb_build_object(
 'telegram_sender_confirmed',false,
 'telegram_recipient_confirmation_required',true,
 'telegram_sender_id',COALESCE(s.telegram_id,0),
 'telegram_recipient_user_id',r.id,
 'telegram_recipient_id',COALESCE(r.telegram_id,0)
), version=d.version+1
FROM users s
LEFT JOIN users r ON r.active AND r.role='collector'
WHERE d.status='draft' AND d.kind='transfer'
 AND d.payload->>'telegram_confirmation_required'='true'
 AND s.id=d.created_by AND r.custodian_id::text=d.payload->>'to_custodian_id';
-- No valid recipient binding: keep the old draft unconfirmable in the new flow.
UPDATE drafts SET payload=payload || '{"telegram_sender_confirmed":false,"telegram_recipient_confirmation_required":true}'::jsonb,
 version=version+1
WHERE status='draft' AND kind='transfer' AND payload->>'telegram_confirmation_required'='true'
 AND NOT payload ? 'telegram_recipient_user_id';
INSERT INTO schema_migrations(version) VALUES (23);
