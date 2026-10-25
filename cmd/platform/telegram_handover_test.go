package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func handoverFixture(t *testing.T) (*App, User, telegramActor, telegramActor, string, *fakeTelegram) {
	t.Helper()
	a, chief, sender, other, fake := transferFixture(t)
	// Synthetic holder with the policy's stable identity; no production data.
	if _, err := a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,'Тестовый отправитель','collector')", telegramTonyCustodianID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE users SET custodian_id=$1 WHERE id=$2", telegramTonyCustodianID, sender.ID); err != nil {
		t.Fatal(err)
	}
	// Move the synthetic opening cash to the policy holder before creating events.
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = put(tx, "test_funding", id(), "handover-funding-"+id(), chief.ID, time.Now(), time.Now(), []Posting{
		{Account: "1200", Custodian: telegramTonyCustodianID, Side: "debit", Amount: 10000},
		{Account: "1200", Custodian: sender.CustodianID, Side: "credit", Amount: 10000},
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sender.CustodianID = telegramTonyCustodianID
	var recipient string
	if err = a.db.QueryRow("SELECT id FROM custodians WHERE kind='chief'").Scan(&recipient); err != nil {
		t.Fatal(err)
	}
	return a, chief, sender, other, recipient, fake
}

func newHandover(t *testing.T, a *App, updateID int64, amount string) string {
	t.Helper()
	if code := telegramRequest(t, a, telegramMessageUpdate(updateID, 555, "/handover@synthetic_bot "+amount)); code != 200 {
		t.Fatal(code)
	}
	var draftID string
	if err := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key=$1 AND kind='handover'", fmt.Sprintf("telegram:%d", updateID)).Scan(&draftID); err != nil {
		t.Fatal("handover not created", err)
	}
	return draftID
}

func assertHandoverState(t *testing.T, a *App, draftID, status string, reserved, entries int) {
	t.Helper()
	var gotStatus string
	var gotReserved, gotEntries int
	if err := a.db.QueryRow("SELECT status FROM drafts WHERE id=$1", draftID).Scan(&gotStatus); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM cash_reservations WHERE draft_id=$1 AND released_at IS NULL", draftID).Scan(&gotReserved); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='handover' AND event_id=$1", draftID).Scan(&gotEntries); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotReserved != reserved || gotEntries != entries {
		t.Fatal("state", gotStatus, gotReserved, gotEntries, "want", status, reserved, entries)
	}
}

func TestTelegramHandoverSenderConfirmationAndReversal(t *testing.T) {
	a, chief, sender, other, recipient, fake := handoverFixture(t)
	draftID := newHandover(t, a, 7101, "0,05к")
	if !fake.contains("Подтвердите передачу главному администратору") {
		t.Fatal("handover preview missing")
	}
	if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "50"}); code != 409 {
		t.Fatal("chief bypassed sender confirmation", code)
	}
	if _, remove := a.telegramCallback(other, telegramTestGroup, 500, "confirm:"+draftID); remove {
		t.Fatal("other collector confirmed handover")
	}
	if _, remove := a.telegramCallback(sender, telegramTestGroup-1, 500, "confirm:"+draftID); remove {
		t.Fatal("wrong chat confirmed handover")
	}
	if message, remove := a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draftID); !remove {
		t.Fatal("sender confirmation failed", message)
	}
	assertHandoverState(t, a, draftID, "posted", 0, 1)
	if message, done := a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draftID); !done || !strings.Contains(message, "уже обработана") {
		t.Fatal("sender retry", message)
	}

	if code := telegramRequest(t, a, telegramMessageUpdate(7101, 555, "/handover 0,05к")); code != 200 {
		t.Fatal(code)
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM drafts WHERE kind='handover'").Scan(&count); err != nil || count != 1 {
		t.Fatal("message retry created duplicate", count, err)
	}
	if code, _ := req(t, a.confirmDraft, other.User, M{"id": draftID, "version": "1", "confirm_amount": "50"}); code != 403 {
		t.Fatal("collector posted handover", code)
	}
	if code, out := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "50"}); code != 200 || out["status"] != "already_posted" {
		t.Fatal("chief retry", code, out)
	}
	assertHandoverState(t, a, draftID, "posted", 0, 1)
	tx, _ := a.tx()
	left, err1 := balance(tx, "1200", "custodian", sender.CustodianID)
	received, err2 := balance(tx, "1210", "custodian", recipient)
	tx.Rollback()
	if err1 != nil || err2 != nil || left != 5000 || received != 5000 {
		t.Fatal("incorrect custody balances", left, received, err1, err2)
	}
	if code, out := req(t, a.reverseDraft, chief, M{"id": draftID, "reason": "Тестовое исправление"}); code != 200 {
		t.Fatal("reversal failed", code, out)
	}
	tx, _ = a.tx()
	left, _ = balance(tx, "1200", "custodian", sender.CustodianID)
	received, _ = balance(tx, "1210", "custodian", recipient)
	tx.Rollback()
	if left != 10000 || received != 0 {
		t.Fatal("reversal did not restore custody", left, received)
	}
}

func TestTelegramHandoverAccessAndInvalidAmounts(t *testing.T) {
	a, _, sender, other, _, fake := handoverFixture(t)
	if _, err := a.db.Exec("UPDATE users SET name='Тони Сопрано' WHERE id=$1", other.ID); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"/handover 50", "/handover"} {
		telegramRequest(t, a, telegramMessageUpdate(int64(7200+i), 556, text))
	}
	if !fake.contains("только Тони Сопрано") {
		t.Fatal("name impersonation did not fail")
	}
	for i, amount := range []string{"0", "-1", "abc", "1 2", "9223372036854775808"} {
		telegramRequest(t, a, telegramMessageUpdate(int64(7210+i), 555, "/handover "+amount))
	}
	telegramRequest(t, a, telegramMessageUpdate(7220, 555, "/handover"))
	if !fake.contains("100000 или 100к") {
		t.Fatal("amount hint missing")
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM drafts WHERE kind='handover'").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid or unauthorized command created draft", count, err)
	}
	draftID := newHandover(t, a, 7221, "1")
	if message, remove := a.telegramCallback(sender, telegramTestGroup, 500, "reject:"+draftID); !remove {
		t.Fatal(message)
	}
	assertHandoverState(t, a, draftID, "rejected", 0, 0)
}

func TestTelegramHandoverRevalidatesParticipants(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE users SET active=false WHERE telegram_id=555",
		"UPDATE users SET role='operator' WHERE telegram_id=555",
		"UPDATE users SET telegram_id=557 WHERE telegram_id=555",
		"UPDATE users SET custodian_id=NULL WHERE telegram_id=555",
		"UPDATE custodians SET active=false WHERE kind='collector'",
		"UPDATE custodians SET active=false WHERE kind='chief'",
	} {
		t.Run(mutation, func(t *testing.T) {
			a, chief, sender, _, _, _ := handoverFixture(t)
			draftID := newHandover(t, a, 7301, "50")
			if _, err := a.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, remove := a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draftID); remove {
				t.Fatal("stale sender binding accepted")
			}
			if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "50"}); code != 409 {
				t.Fatal("changed participants posted", code)
			}
			assertHandoverState(t, a, draftID, "draft", 0, 0)
		})
	}
}

func TestTelegramHandoverProtectsTransferReservations(t *testing.T) {
	a, chief, sender, _, _, _ := handoverFixture(t)
	transfer := newTransfer(t, a, 7401, "60")
	confirmTransferSender(t, a, sender, transfer)
	draftID := newHandover(t, a, 7402, "50")
	if _, done := a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draftID); done {
		t.Fatal("handover spent reserved cash")
	}
	assertHandoverState(t, a, draftID, "draft", 0, 0)
	if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "40"}); code != 409 {
		t.Fatal("chief bypassed sender", code)
	}
	available := newHandover(t, a, 7403, "40")
	if message, done := a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+available); !done {
		t.Fatal(message)
	}
	assertHandoverState(t, a, available, "posted", 0, 1)
	assertTransferState(t, a, transfer, "draft", 1, 0)
}

func TestTelegramHandoverWithProductionStylePrivileges(t *testing.T) {
	a, chief, sender, _, _, _ := handoverFixture(t)
	const role = "metallist_handover_limited_test"
	if _, err := a.db.Exec(`CREATE ROLE metallist_handover_limited_test;
 GRANT USAGE ON SCHEMA public TO metallist_handover_limited_test;
 GRANT SELECT,INSERT,UPDATE ON telegram_operation_messages TO metallist_handover_limited_test;
 GRANT SELECT,INSERT,UPDATE,DELETE ON telegram_input_errors TO metallist_handover_limited_test;
 GRANT SELECT ON ALL TABLES IN SCHEMA public TO metallist_handover_limited_test;
 GRANT INSERT ON drafts,journal_entries,postings,audit_events TO metallist_handover_limited_test;
 GRANT UPDATE(telegram_id,custodian_id) ON users TO metallist_handover_limited_test;
 GRANT UPDATE(payload,status,confirmed_by,confirmed_at,confirmed_amount_cents) ON drafts TO metallist_handover_limited_test;
 GRANT DELETE ON telegram_dialogs TO metallist_handover_limited_test;`); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["role"] = role
	db := sql.OpenDB(stdlib.GetConnector(*cfg))
	t.Cleanup(func() {
		db.Close()
		if _, err := a.db.Exec("DROP OWNED BY " + role + "; DROP ROLE " + role); err != nil {
			t.Error(err)
		}
	})
	limited := &App{db: db, storage: a.storage, location: a.location}
	var currentRole string
	var canUpdateCustodian bool
	if err = db.QueryRow("SELECT current_user,has_table_privilege(current_user,'custodians','UPDATE')").Scan(&currentRole, &canUpdateCustodian); err != nil || currentRole != role || canUpdateCustodian {
		t.Fatal("wrong restricted role", currentRole, canUpdateCustodian, err)
	}
	var update telegramUpdate
	raw := encode(telegramMessageUpdate(7501, 555, "/handover 50"))
	if err = json.Unmarshal(raw, &update); err != nil {
		t.Fatal(err)
	}
	if err = limited.telegramHandover(sender, update.Message, 7501, "50"); err != nil {
		t.Fatal("creation with limited privileges", err)
	}
	var draftID string
	if err = a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:7501'").Scan(&draftID); err != nil {
		t.Fatal(err)
	}
	if message, remove := limited.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draftID); !remove {
		t.Fatal("sender confirmation with limited privileges", message)
	}
	if code, out := req(t, limited.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "49.99"}); code != 200 {
		t.Fatal("receipt with limited privileges", code, out)
	}
	assertHandoverState(t, a, draftID, "posted", 0, 1)
}

func TestTelegramHandoverLegacyPreviewRequiresNewConsent(t *testing.T) {
	a, chief, sender, _, _, _ := handoverFixture(t)
	draft := newHandover(t, a, 7601, "50")
	if _, err := a.db.Exec("UPDATE drafts SET payload=(payload-'telegram_handover_single_confirmation') || '{\"telegram_sender_confirmed\":true}'::jsonb WHERE id=$1", draft); err != nil {
		t.Fatal(err)
	}
	if message, done := a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draft); done || !strings.Contains(message, "Условия передачи изменились") {
		t.Fatal(message, done)
	}
	assertHandoverState(t, a, draft, "draft", 0, 0)
	// Preserve the old, explicitly consented web receipt, including partial amount.
	if code, out := req(t, a.confirmDraft, chief, M{"id": draft, "version": "1", "confirm_amount": "49.99"}); code != 200 {
		t.Fatal(code, out)
	}
	assertHandoverState(t, a, draft, "posted", 0, 1)
}

func TestTelegramHandoverAuditFailureRollsBackImmediatePosting(t *testing.T) {
	a, _, sender, _, _, _ := handoverFixture(t)
	draft := newHandover(t, a, 7701, "50")
	if _, err := a.db.Exec(`CREATE FUNCTION reject_handover_audit_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='draft_confirm' AND NEW.object_type='handover' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_handover_audit_test BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_handover_audit_test();`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.db.Exec("DROP TRIGGER reject_handover_audit_test ON audit_events; DROP FUNCTION reject_handover_audit_test();")
	})
	if _, done := a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draft); done {
		t.Fatal("posted without audit")
	}
	assertHandoverState(t, a, draft, "draft", 0, 0)
}
