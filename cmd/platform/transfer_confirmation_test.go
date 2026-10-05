package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func privateTransferButton(updateID, telegramID int64, action, draftID string) M {
	return M{"update_id": updateID, "callback_query": M{"id": "private-callback", "data": action + ":" + draftID, "from": M{"id": telegramID}, "message": M{"message_id": int64(600), "chat": M{"id": telegramID, "type": "private"}}}}
}
func transferFixture(t *testing.T) (*App, User, telegramActor, telegramActor, *fakeTelegram) {
	t.Helper()
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	_, sender, from, fake := telegramFixture(t, a, card)
	to := id()
	recipient := User{ID: id(), Login: "receiver", Name: "Получатель", Role: "collector"}
	if _, err := a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,'Получатель','collector')", to); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash,telegram_id,custodian_id) VALUES($1,'receiver','Получатель','collector','x',556,$2)", recipient.ID, to); err != nil {
		t.Fatal(err)
	}
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = put(tx, "test_funding", id(), "transfer-funding-"+id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1200", Custodian: from, Side: "debit", Amount: 10000}, {Account: "3100", Side: "credit", Amount: 10000}}, ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return a, chief, telegramActor{User: sender, CustodianID: from}, telegramActor{User: recipient, CustodianID: to}, fake
}
func newTransfer(t *testing.T, a *App, updateID int64, amount string) string {
	t.Helper()
	if code := telegramRequest(t, a, telegramMessageUpdate(updateID, 555, "/transfer 1 "+amount)); code != 200 {
		t.Fatal(code)
	}
	var draftID string
	if err := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key=$1", fmt.Sprintf("telegram:%d", updateID)).Scan(&draftID); err != nil {
		t.Fatal(err)
	}
	return draftID
}
func confirmTransferSender(t *testing.T, a *App, sender telegramActor, draftID string) {
	t.Helper()
	message, _ := a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draftID)
	var held int
	if err := a.db.QueryRow("SELECT count(*) FROM cash_reservations WHERE draft_id=$1 AND released_at IS NULL", draftID).Scan(&held); err != nil || held != 1 {
		t.Fatal("sender did not reserve", message, err)
	}
}
func assertTransferState(t *testing.T, a *App, draftID, status string, reserved, entries int) {
	t.Helper()
	var gotStatus string
	var gotReserved, gotEntries int
	if err := a.db.QueryRow("SELECT status FROM drafts WHERE id=$1", draftID).Scan(&gotStatus); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM cash_reservations WHERE draft_id=$1 AND released_at IS NULL", draftID).Scan(&gotReserved); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='transfer' AND event_id=$1", draftID).Scan(&gotEntries); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotReserved != reserved || gotEntries != entries {
		t.Fatal("state", gotStatus, gotReserved, gotEntries, "want", status, reserved, entries)
	}
}

func TestTransferReservationBlocksAllCashOutflows(t *testing.T) {
	a, chief, sender, recipient, _ := transferFixture(t)
	draftID := newTransfer(t, a, 12001, "70,01")
	assertTransferState(t, a, draftID, "draft", 0, 0)
	confirmTransferSender(t, a, sender, draftID)
	assertTransferState(t, a, draftID, "draft", 1, 0)
	// Other reservations cannot use already-held cash.
	another := newTransfer(t, a, 12002, "30,00")
	a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+another)
	assertTransferState(t, a, another, "draft", 0, 0)
	// Shared posting boundary covers every cash withdrawal, including reversals.
	for _, kind := range []string{"expense", "repayment", "handover", "transfer", "shortage", "reversal"} {
		tx, err := a.tx()
		if err != nil {
			t.Fatal(err)
		}
		_, err = put(tx, kind, id(), "held-"+id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1200", Custodian: sender.CustodianID, Side: "credit", Amount: 3000}, {Account: "5900", Side: "debit", Amount: 3000}}, "")
		tx.Rollback()
		if err == nil {
			t.Fatal("spent reservation", kind)
		}
	}
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	available, err := availableCash(tx, "1200", sender.CustodianID)
	tx.Rollback()
	if err != nil || available != 2999 {
		t.Fatal("available", available, err)
	}
	// Chief can accept instead of the recipient, even before private /start.
	if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "2", "confirm_amount": "70,00"}); code != 409 {
		t.Fatal("partial acceptance allowed", code)
	}
	if code, out := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "2", "confirm_amount": "70,01"}); code != 200 {
		t.Fatal("chief acceptance", code, out)
	}
	assertTransferState(t, a, draftID, "posted", 0, 1)
	if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "2", "confirm_amount": "70,01"}); code != 200 {
		t.Fatal("chief retry", code)
	}
	tx, err = a.tx()
	if err != nil {
		t.Fatal(err)
	}
	from, _ := balance(tx, "1200", "custodian", sender.CustodianID)
	to, _ := balance(tx, "1200", "custodian", recipient.CustodianID)
	tx.Rollback()
	if from != 2999 || to != 7001 {
		t.Fatal("custody", from, to)
	}
	var accepted string
	if err = a.db.QueryRow("SELECT payload->>'telegram_transfer_accepted_by' FROM drafts WHERE id=$1", draftID).Scan(&accepted); err != nil || accepted != "chief" {
		t.Fatal("override audit", accepted, err)
	}
	if code, _ := req(t, a.reverseDraft, chief, M{"id": draftID, "reason": "Исправление"}); code != 200 {
		t.Fatal("reversal", code)
	}
	tx, err = a.tx()
	if err != nil {
		t.Fatal(err)
	}
	from, _ = balance(tx, "1200", "custodian", sender.CustodianID)
	to, _ = balance(tx, "1200", "custodian", recipient.CustodianID)
	tx.Rollback()
	if from != 10000 || to != 0 {
		t.Fatal("reversal custody", from, to)
	}
}

func TestTransferCancellationReleasesReservation(t *testing.T) {
	for _, by := range []string{"sender", "recipient", "chief"} {
		t.Run(by, func(t *testing.T) {
			a, chief, sender, recipient, _ := transferFixture(t)
			draftID := newTransfer(t, a, 13001, "60,00")
			confirmTransferSender(t, a, sender, draftID)
			switch by {
			case "sender":
				a.telegramCallback(sender, telegramTestGroup, 500, "reject:"+draftID)
			case "recipient":
				telegramRequest(t, a, telegramMessageUpdateInChat(13002, 556, 556, "private", "/start"))
				a.telegramRecipientTransfer(recipient, 556, 556, "decline:"+draftID)
			case "chief":
				if code, out := req(t, a.rejectDraft, chief, M{"id": draftID, "version": "2", "reason": "Отмена"}); code != 200 {
					t.Fatal(code, out)
				}
			}
			assertTransferState(t, a, draftID, "rejected", 0, 0)
			a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draftID)
			a.telegramRecipientTransfer(recipient, 556, 556, "receive:"+draftID)
			assertTransferState(t, a, draftID, "rejected", 0, 0)
			second := newTransfer(t, a, 13003, "100,00")
			confirmTransferSender(t, a, sender, second)
		})
	}
}

func TestTransferPrivateAuthorizationAndBindingChanges(t *testing.T) {
	a, chief, sender, recipient, _ := transferFixture(t)
	draftID := newTransfer(t, a, 14001, "50,00")
	if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "50.00"}); code != 409 {
		t.Fatal("chief bypassed sender", code)
	}
	confirmTransferSender(t, a, sender, draftID)
	a.telegramRecipientTransfer(recipient, 556, 556, "receive:"+draftID)
	assertTransferState(t, a, draftID, "draft", 1, 0) // /start required
	telegramRequest(t, a, telegramMessageUpdateInChat(14002, 556, 556, "private", "/start"))
	telegramRequest(t, a, telegramMessageUpdateInChat(14003, 555, 555, "private", "/start"))
	a.telegramRecipientTransfer(sender, 555, 555, "receive:"+draftID)
	telegramRequest(t, a, telegramButtonUpdate(14004, 556, "receive", draftID))
	assertTransferState(t, a, draftID, "draft", 1, 0) // neither another user nor group callback
	if _, err := a.db.Exec("UPDATE users SET telegram_id=557 WHERE id=$1", recipient.ID); err != nil {
		t.Fatal(err)
	}
	a.telegramRecipientTransfer(recipient, 557, 557, "receive:"+draftID)
	if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "2", "confirm_amount": "50.00"}); code != 409 {
		t.Fatal("chief accepted changed binding", code)
	}
	assertTransferState(t, a, draftID, "draft", 1, 0)
}

func TestTransferPostingFailurePreservesReservation(t *testing.T) {
	a, _, sender, recipient, _ := transferFixture(t)
	draftID := newTransfer(t, a, 15001, "50,00")
	confirmTransferSender(t, a, sender, draftID)
	telegramRequest(t, a, telegramMessageUpdateInChat(15002, 556, 556, "private", "/start"))
	if _, err := a.db.Exec(`CREATE FUNCTION fail_transfer_post() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='posted' THEN RAISE EXCEPTION 'synthetic posting failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_transfer_post BEFORE UPDATE ON drafts FOR EACH ROW EXECUTE FUNCTION fail_transfer_post()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.db.Exec("DROP TRIGGER IF EXISTS fail_transfer_post ON drafts; DROP FUNCTION IF EXISTS fail_transfer_post()")
	})
	a.telegramRecipientTransfer(recipient, 556, 556, "receive:"+draftID)
	assertTransferState(t, a, draftID, "draft", 1, 0)
	if _, err := a.db.Exec("DROP TRIGGER fail_transfer_post ON drafts; DROP FUNCTION fail_transfer_post()"); err != nil {
		t.Fatal(err)
	}
	a.telegramRecipientTransfer(recipient, 556, 556, "receive:"+draftID)
	assertTransferState(t, a, draftID, "posted", 0, 1)
}

func TestTransferConcurrentConfirmAndCancel(t *testing.T) {
	a, _, sender, recipient, _ := transferFixture(t)
	draftID := newTransfer(t, a, 16001, "50,00")
	confirmTransferSender(t, a, sender, draftID)
	telegramRequest(t, a, telegramMessageUpdateInChat(16002, 556, 556, "private", "/start"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				a.telegramRecipientTransfer(recipient, 556, 556, "receive:"+draftID)
			} else {
				a.telegramCallback(sender, telegramTestGroup, 500, "reject:"+draftID)
			}
		}(i)
	}
	wg.Wait()
	var status string
	if err := a.db.QueryRow("SELECT status FROM drafts WHERE id=$1", draftID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	entries := 0
	if status == "posted" {
		entries = 1
	} else if status != "rejected" {
		t.Fatal("no terminal state", status)
	}
	assertTransferState(t, a, draftID, status, 0, entries)
	var unbalanced int
	if err := a.db.QueryRow("SELECT count(*) FROM (SELECT entry_id FROM postings GROUP BY entry_id HAVING sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END)<>0) x").Scan(&unbalanced); err != nil || unbalanced != 0 {
		t.Fatal("unbalanced", unbalanced, err)
	}
}

func TestTransferDeliveryRetryAndRestart(t *testing.T) {
	a, _, sender, recipient, fake := transferFixture(t)
	draftID := newTransfer(t, a, 17001, "50,00")
	confirmTransferSender(t, a, sender, draftID)
	if err := a.deliverTransferNotifications(10); err != nil {
		t.Fatal(err)
	}
	var state, lastError string
	if err := a.db.QueryRow("SELECT state,last_error FROM transfer_notifications WHERE draft_id=$1 AND purpose='recipient_request'", draftID).Scan(&state, &lastError); err != nil || state != "pending" || lastError == "" {
		t.Fatal("missing-chat retry", state, lastError, err)
	}
	if fake.contains("Подтвердите фактическое получение") {
		t.Fatal("sent without /start")
	}
	telegramRequest(t, a, telegramMessageUpdateInChat(17002, 556, 556, "private", "/start"))
	var failDelivery atomic.Bool
	failDelivery.Store(true)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body M
		json.NewDecoder(r.Body).Decode(&body)
		if failDelivery.Load() {
			w.WriteHeader(500)
			w.Write([]byte(`{"ok":false}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()
	t.Setenv("TELEGRAM_API_BASE", api.URL)
	if err := a.deliverTransferNotifications(10); err != nil {
		t.Fatal(err)
	}
	assertTransferState(t, a, draftID, "draft", 1, 0)
	if err := a.db.QueryRow("SELECT state,last_error FROM transfer_notifications WHERE draft_id=$1 AND purpose='recipient_request'", draftID).Scan(&state, &lastError); err != nil || state != "pending" || lastError == "" {
		t.Fatal("API-failure retry", state, lastError, err)
	}
	failDelivery.Store(false)
	if _, err := a.db.Exec("UPDATE transfer_notifications SET next_attempt_at=now() WHERE state='pending'"); err != nil {
		t.Fatal(err)
	}
	// New App instance has no in-memory confirmation or delivery state.
	restarted := &App{db: a.db, storage: a.storage, location: a.location}
	if err := restarted.deliverTransferNotifications(10); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT state,last_error FROM transfer_notifications WHERE draft_id=$1 AND purpose='recipient_request'", draftID).Scan(&state, &lastError); err != nil || state != "sent" || lastError != "" {
		t.Fatal("restart delivery", state, lastError, err)
	}
	restarted.telegramRecipientTransfer(recipient, 556, 556, "receive:"+draftID)
	assertTransferState(t, a, draftID, "posted", 0, 1)
	if err := restarted.deliverTransferNotifications(10); err != nil {
		t.Fatal(err)
	}
	var sent int
	if err := a.db.QueryRow("SELECT count(*) FROM transfer_notifications WHERE draft_id=$1 AND purpose IN ('group_final','recipient_final') AND state='sent'", draftID).Scan(&sent); err != nil || sent != 2 {
		t.Fatal("final delivery", sent, err)
	}
}

func TestTransferConcurrentReservationsCannotOverbook(t *testing.T) {
	a, _, sender, _, _ := transferFixture(t)
	first := newTransfer(t, a, 18001, "60,00")
	second := newTransfer(t, a, 18002, "60,00")
	var wg sync.WaitGroup
	for _, draftID := range []string{first, second} {
		wg.Add(1)
		go func(draftID string) {
			defer wg.Done()
			a.telegramCallback(sender, telegramTestGroup, 500, "confirm:"+draftID)
		}(draftID)
	}
	wg.Wait()
	var held int64
	if err := a.db.QueryRow("SELECT COALESCE(sum(amount_cents),0) FROM cash_reservations WHERE released_at IS NULL").Scan(&held); err != nil || held != 6000 {
		t.Fatal("overbooked", held, err)
	}
}

func TestTransferReservationBlocksTelegramSalary(t *testing.T) {
	a, _, sender, _, _ := transferFixture(t)
	transferID := newTransfer(t, a, 19001, "70,01")
	confirmTransferSender(t, a, sender, transferID)
	telegramRequest(t, a, telegramMessageUpdate(19002, 555, "зп 30 За сентябрь"))
	var salaryID string
	if err := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:19002'").Scan(&salaryID); err != nil {
		t.Fatal(err)
	}
	telegramRequest(t, a, telegramButtonUpdate(19003, 555, "confirm", salaryID))
	var status string
	if err := a.db.QueryRow("SELECT status FROM drafts WHERE id=$1", salaryID).Scan(&status); err != nil || status != "draft" {
		t.Fatal("salary spent held cash", status, err)
	}
	assertTransferState(t, a, transferID, "draft", 1, 0)
	a.telegramCallback(sender, telegramTestGroup, 500, "reject:"+transferID)
	telegramRequest(t, a, telegramButtonUpdate(19004, 555, "confirm", salaryID))
	if err := a.db.QueryRow("SELECT status FROM drafts WHERE id=$1", salaryID).Scan(&status); err != nil || status != "posted" {
		t.Fatal("released cash not spendable", status, err)
	}
}

func TestTransferConfirmationWithProductionStylePrivileges(t *testing.T) {
	a, _, sender, recipient, _ := transferFixture(t)
	// testApp has already proved effective disposable database/host/user identity.
	const role = "metallist_transfer_limited_test"
	if _, err := a.db.Exec(`CREATE ROLE metallist_transfer_limited_test;
 GRANT USAGE ON SCHEMA public TO metallist_transfer_limited_test;
 GRANT SELECT ON users,custodians TO metallist_transfer_limited_test;
 GRANT UPDATE(telegram_id,custodian_id) ON users TO metallist_transfer_limited_test;
 GRANT SELECT,INSERT,UPDATE ON drafts,cash_reservations,transfer_notifications,telegram_private_chats TO metallist_transfer_limited_test;
 GRANT SELECT,INSERT ON journal_entries,postings,audit_events TO metallist_transfer_limited_test;`); err != nil {
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
	var name, currentRole string
	var canUpdateCustodian bool
	if err = db.QueryRow("SELECT current_database(),current_user,has_table_privilege(current_user,'custodians','UPDATE')").Scan(&name, &currentRole, &canUpdateCustodian); err != nil || name != "metallist_platform_test" || currentRole != role || canUpdateCustodian {
		t.Fatal("unsafe or non-representative restricted role", name, currentRole, canUpdateCustodian, err)
	}
	draftID := newTransfer(t, a, 20001, "50,00")
	confirmTransferSender(t, limited, sender, draftID)
	assertTransferState(t, a, draftID, "draft", 1, 0)
	if code := telegramRequest(t, limited, telegramMessageUpdateInChat(20002, 556, 556, "private", "/start")); code != 200 {
		t.Fatal("limited-role /start", code)
	}
	message, _ := limited.telegramRecipientTransfer(recipient, 556, 556, "receive:"+draftID)
	assertTransferState(t, a, draftID, "posted", 0, 1)
	if message != "Получение подтверждено. Перевод проведён" {
		t.Fatal(message)
	}
	// A database permission failure must not masquerade as a changed binding.
	if _, err = a.db.Exec("REVOKE SELECT ON custodians FROM " + role); err != nil {
		t.Fatal(err)
	}
	tx, err := limited.tx()
	if err != nil {
		t.Fatal(err)
	}
	p := M{"telegram_sender_user_id": sender.ID, "from_custodian_id": sender.CustodianID, "telegram_sender_id": int64(555), "telegram_recipient_user_id": recipient.ID, "to_custodian_id": recipient.CustodianID, "telegram_recipient_id": int64(556)}
	err = validateTransferParties(tx, p)
	tx.Rollback()
	if err == nil || publicError(http.StatusConflict, err).code != "DATABASE_ERROR" {
		t.Fatal("database failure misclassified", err)
	}
}
