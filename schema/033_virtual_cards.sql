-- Expand only. Apply transactionally after the deployed schema 032.
DO $$ BEGIN
 IF (SELECT max(version) FROM schema_migrations) <> 32 THEN
  RAISE EXCEPTION 'schema 033 requires version 32';
 END IF;
END $$;
ALTER TABLE cards ADD COLUMN physical_card_id uuid REFERENCES cards(id) ON DELETE RESTRICT;
ALTER TABLE cards ADD CONSTRAINT cards_physical_not_self CHECK(physical_card_id IS DISTINCT FROM id);
CREATE INDEX cards_physical_card_idx ON cards(physical_card_id) WHERE physical_card_id IS NOT NULL;

-- Same serialization boundary as application financial commands, also for SQL writes.
CREATE FUNCTION guard_card_balance_link() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(704112);
 IF TG_OP='UPDATE' AND NEW.physical_card_id IS NOT DISTINCT FROM OLD.physical_card_id THEN
  RETURN NEW;
 END IF;
 IF NEW.physical_card_id IS NOT NULL THEN
  IF NOT EXISTS(SELECT 1 FROM cards WHERE id=NEW.physical_card_id AND physical_card_id IS NULL) THEN
   RAISE EXCEPTION 'Привязать можно только к пластиковой карте';
  END IF;
  IF EXISTS(SELECT 1 FROM cards WHERE physical_card_id=NEW.id) THEN
   RAISE EXCEPTION 'У карты уже есть виртуальные карты';
  END IF;
 END IF;
 IF TG_OP='UPDATE' AND (
  EXISTS(SELECT 1 FROM payment_request_rows WHERE card_id=OLD.id)
  OR EXISTS(SELECT 1 FROM registry_rows WHERE card_id=OLD.id)
  OR EXISTS(SELECT 1 FROM postings WHERE card_id=OLD.id)
  OR EXISTS(SELECT 1 FROM observations WHERE card_id=OLD.id)
  OR EXISTS(SELECT 1 FROM drafts WHERE payload->>'card_id'=OLD.id::text
   OR (payload->>'source_kind'='card' AND payload->>'source_id'=OLD.id::text)
   OR (payload->>'destination_kind'='card' AND payload->>'destination_id'=OLD.id::text)
   OR EXISTS(SELECT 1 FROM jsonb_array_elements((CASE WHEN jsonb_typeof(payload->'withdrawal_items')='array' THEN payload->'withdrawal_items' ELSE '[]'::jsonb END) || (CASE WHEN jsonb_typeof(payload->'expense_items')='array' THEN payload->'expense_items' ELSE '[]'::jsonb END)) item WHERE item->>'card_id'=OLD.id::text))
 ) THEN
  RAISE EXCEPTION 'Привязку нельзя менять: карта уже использована';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER cards_balance_link_guard BEFORE INSERT OR UPDATE OF physical_card_id ON cards
 FOR EACH ROW EXECUTE FUNCTION guard_card_balance_link();

-- Keep the instrument on each posting. A shared balance is a projection, never a copy.
CREATE VIEW card_balance_groups AS
 SELECT id AS card_id, COALESCE(physical_card_id,id) AS balance_card_id FROM cards;
CREATE VIEW shared_card_balances AS
 SELECT g.card_id,g.balance_card_id,COALESCE(b.amount_cents,0) AS amount_cents
 FROM card_balance_groups g LEFT JOIN (
  SELECT m.balance_card_id,SUM(CASE WHEN p.side='debit' THEN p.amount_cents ELSE -p.amount_cents END) AS amount_cents
  FROM postings p JOIN card_balance_groups m ON m.card_id=p.card_id
  WHERE p.account='1100' GROUP BY m.balance_card_id
 ) b ON b.balance_card_id=g.balance_card_id;
CREATE VIEW shared_card_observations AS
 SELECT g.card_id,g.balance_card_id,o.observed_cents,o.observed_at
 FROM card_balance_groups g LEFT JOIN LATERAL (
  SELECT o.observed_cents,o.observed_at FROM observations o
  JOIN card_balance_groups member ON member.card_id=o.card_id
  WHERE member.balance_card_id=g.balance_card_id ORDER BY o.observed_at DESC,o.id DESC LIMIT 1
 ) o ON true;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='metallist_demo_app') THEN
  GRANT SELECT ON card_balance_groups,shared_card_balances,shared_card_observations TO metallist_demo_app;
  GRANT UPDATE(physical_card_id) ON cards TO metallist_demo_app;
 END IF;
END $$;
INSERT INTO schema_migrations(version) VALUES (33);
