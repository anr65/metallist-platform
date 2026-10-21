package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func repaymentFixture(t *testing.T) (*App, User, telegramActor, telegramActor, string, *fakeTelegram) {
	t.Helper()
	a, chief, sender, other, _, fake := handoverFixture(t)
	var merchant string
	if err := a.db.QueryRow("UPDATE merchants SET name='Мерчант 00',code='PRIVATE-CODE' RETURNING id").Scan(&merchant); err != nil {
		t.Fatal(err)
	}
	tx, _ := a.tx()
	if _, err := put(tx, "test_debt", id(), "repayment-debt-"+id(), chief.ID, time.Now(), time.Now(), []Posting{
		{Account: "3100", Side: "debit", Amount: 7000}, {Account: "2100", Side: "credit", Amount: 7000, Merchant: merchant},
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return a, chief, sender, other, merchant, fake
}

func repaymentDialogForTest(t *testing.T, a *App, userID string) telegramRepaymentDialog {
	t.Helper()
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	d, err := loadRepaymentDialog(tx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func newPrivateRepayment(t *testing.T, a *App, sender telegramActor, updateID int64, amount string) string {
	t.Helper()
	if code := telegramRequest(t, a, telegramMessageUpdateInChat(updateID, 555, 555, "private", "/repay")); code != 200 {
		t.Fatal("start", code)
	}
	d := repaymentDialogForTest(t, a, sender.ID)
	if code := telegramRequest(t, a, privateTransferButton(updateID+1, 555, "rm", d.Token+":0")); code != 200 {
		t.Fatal("merchant", code)
	}
	if code := telegramRequest(t, a, telegramMessageUpdateInChat(updateID+2, 555, 555, "private", amount)); code != 200 {
		t.Fatal("amount", code)
	}
	var draftID string
	if err := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key=$1 AND kind='repayment'", fmt.Sprintf("telegram:%d", updateID+2)).Scan(&draftID); err != nil {
		t.Fatal("draft", err)
	}
	return draftID
}

func repaymentState(t *testing.T, a *App, draftID, wantStatus string, wantEntries, wantNotifications int) {
	t.Helper()
	var status string
	var entries, notifications int
	err := a.db.QueryRow(`SELECT status,(SELECT count(*) FROM journal_entries WHERE event_type='repayment' AND event_id=d.id),
 (SELECT count(*) FROM telegram_repayment_notifications WHERE draft_id=d.id) FROM drafts d WHERE id=$1`, draftID).Scan(&status, &entries, &notifications)
	if err != nil || status != wantStatus || entries != wantEntries || notifications != wantNotifications {
		t.Fatal("repayment state", status, entries, notifications, err)
	}
}

func lastTelegramCall(t *testing.T, fake *fakeTelegram, method string) M {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for i := len(fake.calls) - 1; i >= 0; i-- {
		if str(fake.calls[i], "method") == method {
			return fake.calls[i]
		}
	}
	t.Fatal("missing Telegram method", method)
	return nil
}

func TestTelegramPrivateRepaymentPostsAndNotifies(t *testing.T) {
	a, chief, sender, _, merchant, fake := repaymentFixture(t)
	draftID := newPrivateRepayment(t, a, sender, 8100, "50,01")
	repaymentState(t, a, draftID, "draft", 0, 0)
	if fake.contains("PRIVATE-CODE") || fake.contains("70,00") || fake.contains("4,00") {
		t.Fatal("merchant internals leaked")
	}
	if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "50.01"}); code != 409 {
		t.Fatal("chief bypassed private confirmation", code)
	}
	telegramRequest(t, a, privateTransferButton(8103, 556, "rc", draftID))
	telegramRequest(t, a, telegramButtonUpdate(8104, 555, "rc", draftID))
	repaymentState(t, a, draftID, "draft", 0, 0)
	if code := telegramRequest(t, a, privateTransferButton(8105, 555, "rc", draftID)); code != 200 {
		t.Fatal(code)
	}
	repaymentState(t, a, draftID, "posted", 1, 1)
	var activeDialogs int
	if err := a.db.QueryRow("SELECT count(*) FROM telegram_repayment_dialogs WHERE user_id=$1", sender.ID).Scan(&activeDialogs); err != nil || activeDialogs != 0 {
		t.Fatal("posted flow still consumes private text", activeDialogs, err)
	}
	for i := int64(8106); i < 8109; i++ {
		telegramRequest(t, a, privateTransferButton(i, 555, "rc", draftID))
	}
	telegramRequest(t, a, telegramMessageUpdateInChat(8102, 555, 555, "private", "50,01"))
	repaymentState(t, a, draftID, "posted", 1, 1)
	tx, _ := a.tx()
	cash, _ := balance(tx, "1200", "custodian", sender.CustodianID)
	debt, _ := balance(tx, "2100", "merchant", merchant)
	receivable, _ := balance(tx, "1300", "merchant", merchant)
	tx.Rollback()
	if cash != 4999 || debt != 1999 || receivable != 0 {
		t.Fatal("wrong cash/debt", cash, debt, receivable)
	}
	if err := a.deliverRepaymentNotifications(10); err != nil {
		t.Fatal(err)
	}
	call := lastTelegramCall(t, fake, "sendMessage")
	if call["chat_id"] != float64(telegramTestGroup) || !strings.Contains(str(call, "text"), "Мерчант 00") || !strings.Contains(str(call, "text"), "50,01") || !strings.Contains(str(call, "text"), "Тони Сопрано") {
		t.Fatal("wrong group notification", call)
	}
	fake.mu.Lock()
	before := len(fake.calls)
	fake.mu.Unlock()
	if err := a.deliverRepaymentNotifications(10); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	after := len(fake.calls)
	fake.mu.Unlock()
	if before != after {
		t.Fatal("delivered notification repeated")
	}
	if code, out := req(t, a.reverseDraft, chief, M{"id": draftID, "reason": "Тестовое сторно"}); code != 200 {
		t.Fatal("reversal", code, out)
	}
	tx, _ = a.tx()
	cash, _ = balance(tx, "1200", "custodian", sender.CustodianID)
	debt, _ = balance(tx, "2100", "merchant", merchant)
	tx.Rollback()
	if cash != 10000 || debt != 7000 {
		t.Fatal("reversal did not restore cash/debt", cash, debt)
	}
}

func TestTelegramPrivateRepaymentOverpaymentAndReservations(t *testing.T) {
	a, _, sender, _, merchant, _ := repaymentFixture(t)
	transfer := newTransfer(t, a, 8200, "30")
	confirmTransferSender(t, a, sender, transfer)
	draftID := newPrivateRepayment(t, a, sender, 8201, "80")
	telegramRequest(t, a, privateTransferButton(8204, 555, "rc", draftID))
	repaymentState(t, a, draftID, "draft", 0, 0)
	if _, remove := a.telegramCallback(sender, telegramTestGroup, 500, "reject:"+transfer); !remove {
		t.Fatal("transfer cancellation failed")
	}
	telegramRequest(t, a, privateTransferButton(8205, 555, "rc", draftID))
	repaymentState(t, a, draftID, "posted", 1, 1)
	tx, _ := a.tx()
	cash, _ := balance(tx, "1200", "custodian", sender.CustodianID)
	debt, _ := balance(tx, "2100", "merchant", merchant)
	receivable, _ := balance(tx, "1300", "merchant", merchant)
	tx.Rollback()
	if cash != 2000 || debt != 0 || receivable != 1000 {
		t.Fatal("overpayment accounting", cash, debt, receivable)
	}
	var imbalance, badDimensions int
	if err := a.db.QueryRow("SELECT count(*) FROM (SELECT entry_id FROM postings GROUP BY entry_id HAVING sum(CASE side WHEN 'debit' THEN amount_cents ELSE -amount_cents END)<>0) x").Scan(&imbalance); err != nil || imbalance != 0 {
		t.Fatal("unbalanced postings", imbalance, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM postings p JOIN journal_entries j ON j.id=p.entry_id WHERE j.event_id=$1 AND ((p.account='1200' AND p.custodian_id<>$2) OR (p.account IN ('2100','1300') AND p.merchant_id<>$3) OR p.account IN ('4100','4200','5100'))", draftID, sender.CustodianID, merchant).Scan(&badDimensions); err != nil || badDimensions != 0 {
		t.Fatal("wrong dimensions or P&L impact", badDimensions, err)
	}
}

func TestTelegramPrivateRepaymentNamesAndPagination(t *testing.T) {
	a, _, sender, _, _, fake := repaymentFixture(t)
	for i := 1; i <= 24; i++ {
		if _, err := a.db.Exec("INSERT INTO merchants(id,code,name) VALUES($1,$2,$3)", id(), fmt.Sprintf("PRIVATE-%02d", i), fmt.Sprintf("Мерчант %02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec("INSERT INTO merchants(id,code,name,active) VALUES($1,'PRIVATE-OFF','Неактивный',false)", id()); err != nil {
		t.Fatal(err)
	}
	telegramRequest(t, a, telegramMessageUpdateInChat(8300, 555, 555, "private", "/repay"))
	d := repaymentDialogForTest(t, a, sender.ID)
	firstToken := d.Token
	for page, count := range []int{10, 10, 5} {
		d = repaymentDialogForTest(t, a, sender.ID)
		if d.Page != page || len(d.Merchants) != count || d.HasNext != (page < 2) {
			t.Fatal("page", d.Page, len(d.Merchants), d.HasNext)
		}
		method := "editMessageText"
		if page == 0 {
			method = "sendMessage"
		}
		call := lastTelegramCall(t, fake, method)
		raw, _ := json.Marshal(call["reply_markup"])
		var keyboard struct {
			Rows [][]M `json:"inline_keyboard"`
		}
		if err := json.Unmarshal(raw, &keyboard); err != nil {
			t.Fatal(err)
		}
		merchants := 0
		for _, row := range keyboard.Rows {
			for _, button := range row {
				if strings.HasPrefix(str(button, "callback_data"), "rm:") {
					if !strings.HasPrefix(str(button, "text"), "Мерчант ") || len(str(button, "callback_data")) > 64 {
						t.Fatal("button reveals non-name data", button)
					}
					merchants++
				}
			}
		}
		if merchants != count {
			t.Fatal("wrong number of merchant buttons", merchants)
		}
		if page < 2 {
			telegramRequest(t, a, privateTransferButton(int64(8301+page), 555, "rp", fmt.Sprintf("%s:%d", d.Token, page+1)))
		}
	}
	telegramRequest(t, a, privateTransferButton(8305, 555, "rm", firstToken+":0"))
	if got := repaymentDialogForTest(t, a, sender.ID); got.Selected.ID != "" || got.Page != 2 {
		t.Fatal("old page selected merchant")
	}
	telegramRequest(t, a, privateTransferButton(8306, 555, "rp", d.Token+":1"))
	if got := repaymentDialogForTest(t, a, sender.ID); got.Page != 1 || len(got.Merchants) != 10 {
		t.Fatal("back pagination", got.Page)
	}
	if fake.contains("PRIVATE-") {
		t.Fatal("merchant code leaked")
	}
}

func TestTelegramPrivateRepaymentAccessAndStaleFlow(t *testing.T) {
	a, _, sender, other, _, _ := repaymentFixture(t)
	telegramRequest(t, a, telegramMessageUpdateInChat(8400, 556, 556, "private", "/repay"))
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM telegram_repayment_dialogs WHERE user_id=$1", other.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("other collector obtained merchant list", err)
	}
	telegramRequest(t, a, telegramMessageUpdate(8401, 555, "/repay"))
	if err := a.db.QueryRow("SELECT count(*) FROM telegram_repayment_dialogs").Scan(&count); err != nil || count != 0 {
		t.Fatal("group started private repayment", err)
	}
	draftID := newPrivateRepayment(t, a, sender, 8410, "1")
	telegramRequest(t, a, telegramMessageUpdateInChat(8413, 555, 555, "private", "/cancel"))
	telegramRequest(t, a, privateTransferButton(8414, 555, "rc", draftID))
	repaymentState(t, a, draftID, "rejected", 0, 0)
	draftID = newPrivateRepayment(t, a, sender, 8420, "1")
	telegramRequest(t, a, telegramMessageUpdateInChat(8423, 555, 555, "private", "/repay"))
	telegramRequest(t, a, privateTransferButton(8424, 555, "rc", draftID))
	repaymentState(t, a, draftID, "rejected", 0, 0)
	draftID = newPrivateRepayment(t, a, sender, 8430, "1")
	if _, err := a.db.Exec("UPDATE telegram_repayment_dialogs SET expires_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	telegramRequest(t, a, privateTransferButton(8433, 555, "rc", draftID))
	repaymentState(t, a, draftID, "draft", 0, 0)
}

func TestTelegramPrivateRepaymentRevalidatesAndRejectsInvalidAmounts(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE users SET active=false WHERE telegram_id=555",
		"UPDATE users SET role='operator' WHERE telegram_id=555",
		"UPDATE users SET telegram_id=557 WHERE telegram_id=555",
		"UPDATE users SET custodian_id=NULL WHERE telegram_id=555",
		"UPDATE custodians SET active=false WHERE kind='collector'",
		"UPDATE merchants SET active=false",
		"UPDATE merchants SET name='Другое имя'",
		"UPDATE drafts SET payload=jsonb_set(payload,'{source_id}',to_jsonb('1208f775-a1ff-4d65-b9e2-c60859f4d322'::text)) WHERE kind='repayment'",
	} {
		t.Run(mutation, func(t *testing.T) {
			a, _, sender, _, _, _ := repaymentFixture(t)
			draftID := newPrivateRepayment(t, a, sender, 8500, "1")
			if _, err := a.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, remove := a.telegramRepaymentConfirm(sender, 555, 555, "rc:"+draftID); remove {
				t.Fatal("changed binding or merchant accepted")
			}
			repaymentState(t, a, draftID, "draft", 0, 0)
		})
	}
	a, _, sender, _, _, _ := repaymentFixture(t)
	telegramRequest(t, a, telegramMessageUpdateInChat(8550, 555, 555, "private", "/repay"))
	d := repaymentDialogForTest(t, a, sender.ID)
	telegramRequest(t, a, privateTransferButton(8551, 555, "rm", d.Token+":0"))
	for i, input := range []string{"0", "-1", "abc", "1.001", "9223372036854775808", "1 2"} {
		telegramRequest(t, a, telegramMessageUpdateInChat(int64(8552+i), 555, 555, "private", input))
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM drafts WHERE kind='repayment'").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid amount created repayment", count, err)
	}
}

func TestTelegramPrivateRepaymentDeliveryRetryAndAtomicity(t *testing.T) {
	a, _, sender, _, _, fake := repaymentFixture(t)
	draftID := newPrivateRepayment(t, a, sender, 8600, "1")
	// Force an outbox failure: the posting, draft state and audit must roll back.
	if _, err := a.db.Exec(`CREATE FUNCTION fail_repayment_outbox_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic outbox failure'; END $$;
 CREATE TRIGGER fail_repayment_outbox BEFORE INSERT ON telegram_repayment_notifications FOR EACH ROW EXECUTE FUNCTION fail_repayment_outbox_test()`); err != nil {
		t.Fatal(err)
	}
	message, remove := a.telegramRepaymentConfirm(sender, 555, 555, "rc:"+draftID)
	if remove {
		t.Fatal("failed notification persistence committed money", message)
	}
	repaymentState(t, a, draftID, "draft", 0, 0)
	if _, err := a.db.Exec("DROP TRIGGER fail_repayment_outbox ON telegram_repayment_notifications; DROP FUNCTION fail_repayment_outbox_test()"); err != nil {
		t.Fatal(err)
	}
	if message, remove = a.telegramRepaymentConfirm(sender, 555, 555, "rc:"+draftID); !remove {
		t.Fatal(message)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	t.Cleanup(server.Close)
	previous := os.Getenv("TELEGRAM_API_BASE")
	t.Setenv("TELEGRAM_API_BASE", server.URL)
	if err := a.deliverRepaymentNotifications(10); err != nil {
		t.Fatal(err)
	}
	var state, lastError string
	if err := a.db.QueryRow("SELECT state,last_error FROM telegram_repayment_notifications WHERE draft_id=$1", draftID).Scan(&state, &lastError); err != nil || state != "pending" || lastError == "" {
		t.Fatal("failed delivery lost", state, lastError, err)
	}
	t.Setenv("TELEGRAM_API_BASE", previous)
	if _, err := a.db.Exec("UPDATE telegram_repayment_notifications SET next_attempt_at=now() WHERE draft_id=$1", draftID); err != nil {
		t.Fatal(err)
	}
	// A newly constructed App stands in for a restarted process.
	restarted := &App{db: a.db, storage: a.storage, location: a.location}
	if err := restarted.deliverRepaymentNotifications(10); err != nil {
		t.Fatal(err)
	}
	if !fake.contains("Выдача мерчанту") {
		t.Fatal("notification not delivered after retry")
	}
	repaymentState(t, a, draftID, "posted", 1, 1)
}

func TestTelegramPrivateRepaymentConcurrentConfirmation(t *testing.T) {
	a, _, sender, _, _, _ := repaymentFixture(t)
	draftID := newPrivateRepayment(t, a, sender, 8700, "50")
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.telegramRepaymentConfirm(sender, 555, 555, "rc:"+draftID) }()
	}
	wg.Wait()
	repaymentState(t, a, draftID, "posted", 1, 1)
}

func TestTelegramPrivateRepaymentWithProductionStylePrivileges(t *testing.T) {
	a, _, sender, _, _, _ := repaymentFixture(t)
	const role = "metallist_repayment_limited_test"
	if _, err := a.db.Exec(`CREATE ROLE metallist_repayment_limited_test;
 GRANT USAGE ON SCHEMA public TO metallist_repayment_limited_test;
 GRANT SELECT ON ALL TABLES IN SCHEMA public TO metallist_repayment_limited_test;
 GRANT INSERT ON telegram_updates,drafts,journal_entries,postings,audit_events,telegram_repayment_dialogs,telegram_repayment_notifications TO metallist_repayment_limited_test;
 GRANT UPDATE(telegram_id,custodian_id) ON users TO metallist_repayment_limited_test;
 GRANT UPDATE(payload,status,confirmed_by,confirmed_at,confirmed_amount_cents) ON drafts TO metallist_repayment_limited_test;
 GRANT UPDATE,DELETE ON telegram_repayment_dialogs TO metallist_repayment_limited_test;
 GRANT UPDATE(state,attempts,next_attempt_at,last_error) ON telegram_repayment_notifications TO metallist_repayment_limited_test;`); err != nil {
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
	var updateCustodian bool
	if err := db.QueryRow("SELECT current_user,has_table_privilege(current_user,'custodians','UPDATE')").Scan(&currentRole, &updateCustodian); err != nil || currentRole != role || updateCustodian {
		t.Fatal("not representative role", currentRole, updateCustodian, err)
	}
	draftID := newPrivateRepayment(t, limited, sender, 8800, "80")
	if message, remove := limited.telegramRepaymentConfirm(sender, 555, 555, "rc:"+draftID); !remove {
		t.Fatal("limited-role confirmation", message)
	}
	if err := limited.deliverRepaymentNotifications(10); err != nil {
		t.Fatal("limited-role notification", err)
	}
	repaymentState(t, a, draftID, "posted", 1, 1)
}

func TestTelegramPrivateRepaymentPreviewDeliveryRecovery(t *testing.T) {
	a, _, sender, _, _, _ := repaymentFixture(t)
	telegramRequest(t, a, telegramMessageUpdateInChat(8900, 555, 555, "private", "/repay"))
	d := repaymentDialogForTest(t, a, sender.ID)
	telegramRequest(t, a, privateTransferButton(8901, 555, "rm", d.Token+":0"))
	previous := os.Getenv("TELEGRAM_API_BASE")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	t.Cleanup(server.Close)
	t.Setenv("TELEGRAM_API_BASE", server.URL)
	amountUpdate := telegramMessageUpdateInChat(8902, 555, 555, "private", "1,23")
	if code := telegramRequest(t, a, amountUpdate); code != 500 {
		t.Fatal("preview failure not retried", code)
	}
	d = repaymentDialogForTest(t, a, sender.ID)
	repaymentState(t, a, d.DraftID, "draft", 0, 0)
	if _, remove := a.telegramRepaymentConfirm(sender, 555, 555, "rc:"+d.DraftID); remove {
		t.Fatal("undelivered preview posted")
	}
	t.Setenv("TELEGRAM_API_BASE", previous)
	if code := telegramRequest(t, a, amountUpdate); code != 200 {
		t.Fatal("preview retry", code)
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM drafts WHERE kind='repayment'").Scan(&count); err != nil || count != 1 {
		t.Fatal("preview retry duplicated draft", count, err)
	}
	if message, remove := a.telegramRepaymentConfirm(sender, 555, 555, "rc:"+d.DraftID); !remove {
		t.Fatal("delivered preview did not post", message)
	}
	repaymentState(t, a, d.DraftID, "posted", 1, 1)
}

func TestTelegramPrivateRepaymentRepeatedEditAndMenu(t *testing.T) {
	a, _, sender, _, _, fake := repaymentFixture(t)
	if err := a.telegramSetRepaymentCommands(); err != nil {
		t.Fatal(err)
	}
	call := lastTelegramCall(t, fake, "setMyCommands")
	raw, _ := json.Marshal(call)
	if !strings.Contains(string(raw), `"chat_id":555`) || !strings.Contains(string(raw), `"command":"repay"`) || strings.Contains(string(raw), "all_private_chats") {
		t.Fatal("repay menu is not limited to Tony", call)
	}
	telegramRequest(t, a, telegramMessageUpdateInChat(9000, 555, 555, "private", "/repay"))
	d := repaymentDialogForTest(t, a, sender.ID)
	var mu sync.Mutex
	edits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			mu.Lock()
			edits++
			current := edits
			mu.Unlock()
			if current > 1 {
				w.WriteHeader(400)
				w.Write([]byte(`{"ok":false,"description":"Bad Request: message is not modified: same content"}`))
				return
			}
		}
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("TELEGRAM_API_BASE", server.URL)
	update := privateTransferButton(9001, 555, "rm", d.Token+":0")
	for i := 0; i < 2; i++ {
		if code := telegramRequest(t, a, update); code != 200 {
			t.Fatal("repeated edit remains retryable", code)
		}
	}
	if got := repaymentDialogForTest(t, a, sender.ID); got.Selected.ID == "" {
		t.Fatal("selection lost")
	}
}
