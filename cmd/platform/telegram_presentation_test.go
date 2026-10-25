package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func (f *fakeTelegram) countMethod(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if str(call, "method") == method {
			n++
		}
	}
	return n
}
func uiCard(t *testing.T, a *App, draft string, chat int64) int64 {
	t.Helper()
	var msg int64
	if err := a.db.QueryRow("SELECT message_id FROM telegram_operation_messages WHERE draft_id=$1 AND chat_id=$2", draft, chat).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	return msg
}
func uiDraft(t *testing.T, a *App, update int64) string {
	t.Helper()
	var draft string
	if err := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key=$1", fmt.Sprintf("telegram:%d", update)).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	return draft
}

func TestTelegramComfortWithdrawalOneCardAndCommission(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	if telegramRequest(t, a, telegramMessageUpdate(81001, 555, "/withdraw")) != 200 {
		t.Fatal("menu")
	}
	if !fake.contains("Без комиссии: 7898 100к/200") || !fake.contains("С комиссией: 7898 100к/200 комса 2400") {
		t.Fatal("examples")
	}
	var prompt int64
	if err := a.db.QueryRow("SELECT (context->>'prompt_message_id')::bigint FROM telegram_dialogs").Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if telegramRequest(t, a, telegramMessageUpdate(81002, 555, "1234 100к/200 комса2400")) != 200 {
		t.Fatal("input")
	}
	draft := uiDraft(t, a, 81002)
	if uiCard(t, a, draft, telegramTestGroup) != prompt || fake.countMethod("sendMessage") != 1 {
		t.Fatal("preview did not reuse prompt")
	}
	if !fake.contains("Банк. Комиссия с этой карты: 2 400 ₽") {
		t.Fatal("preview fee")
	}
	for i := int64(0); i < 2; i++ {
		if telegramRequest(t, a, telegramButtonUpdate(81003+i, 555, "confirm", draft)) != 200 {
			t.Fatal("confirmation")
		}
	}
	if !fake.contains("✅ Сборщик · снято 100 000 ₽ · ****1234\nБанковская комиссия: 2 400 ₽") || fake.countMethod("sendMessage") != 1 {
		t.Fatal("summary appended or lost fee")
	}
	var entries, observations, fees int
	a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='withdrawal' AND event_id=$1", draft).Scan(&entries)
	a.db.QueryRow("SELECT count(*) FROM observations WHERE source=$1", "telegram:"+draft).Scan(&observations)
	a.db.QueryRow("SELECT count(*) FROM drafts WHERE payload->>'withdrawal_id'=$1", draft).Scan(&fees)
	if entries != 1 || observations != 1 || fees != 1 {
		t.Fatal(entries, observations, fees)
	}
}

func TestTelegramComfortTypedErrorsCleanupIsScoped(t *testing.T) {
	a, _, sender, other, fake := transferFixture(t)
	cases := []struct {
		update, user int64
		text         string
	}{{82001, 555, "/withdraw 1234 abc/0"}, {82002, 555, "/expense 1234 прогрев abc"}, {82003, 556, "/withdraw 1234 abc/0"}}
	for _, c := range cases {
		if telegramRequest(t, a, telegramMessageUpdate(c.update, c.user, c.text)) != 200 {
			t.Fatal(c)
		}
	}
	var errorID int64
	var code string
	if err := a.db.QueryRow("SELECT message_id,code FROM telegram_input_errors WHERE update_id=82001").Scan(&errorID, &code); err != nil || code != "invalid_amount" {
		t.Fatal(code, err)
	}
	if fake.countMethod("deleteMessage") != 0 {
		t.Fatal("deleted unresolved errors")
	}
	if telegramRequest(t, a, telegramMessageUpdate(82004, 555, "/withdraw 1234 1к/0")) != 200 {
		t.Fatal("corrected input")
	}
	var remaining int
	a.db.QueryRow("SELECT count(*) FROM telegram_input_errors WHERE user_id=$1 AND operation='withdraw'", sender.ID).Scan(&remaining)
	if remaining != 0 || fake.countMethod("deleteMessage") != 1 {
		t.Fatal("error not deleted", remaining)
	}
	a.db.QueryRow("SELECT count(*) FROM telegram_input_errors WHERE user_id=$1 OR operation='expense'", other.ID).Scan(&remaining)
	if remaining != 2 {
		t.Fatal("deleted unrelated errors", remaining)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, call := range fake.calls {
		if str(call, "method") == "deleteMessage" && int64(call["message_id"].(float64)) != errorID {
			t.Fatal("deleted source or another actor's message")
		}
	}
}

func TestTelegramComfortTypedRowAndOtherOperations(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, custodian, _ := telegramFixture(t, a, card)
	u := telegramActor{User: collector, CustodianID: custodian}
	_, err := a.telegramParseWithdrawals(u, "1234 1к/0\n1234 лимит")
	var typed *telegramInputError
	if !errors.As(err, &typed) || typed.Row != 2 || typed.Field != "operation" || typed.Code != "wrong_operation" || !strings.Contains(err.Error(), "/card_status 1234 лимит") {
		t.Fatal(err)
	}
	for _, c := range []struct{ op, text, field string }{{"handover", "Укажите положительную сумму передачи", "amount"}, {"transfer", "Получатель недоступен", "recipient"}, {"salary", "Укажите комментарий", "comment"}, {"card_status", "Неизвестный статус карты", "status"}, {"block_card", "Укажите 4 цифры карты", "card"}, {"repay", "Укажите положительную сумму", "amount"}} {
		got := telegramClassifyInputError(c.op, errors.New(c.text))
		if got.Operation != c.op || got.Field != c.field || got.Code == "invalid_input" {
			t.Fatal(c, got)
		}
	}
}

func TestTelegramComfortHandoverSeparateAmountAndWebFinal(t *testing.T) {
	a, chief, sender, _, _, fake := handoverFixture(t)
	if telegramRequest(t, a, telegramMessageUpdate(83001, 555, "/handover")) != 200 {
		t.Fatal("menu")
	}
	telegramRequest(t, a, telegramMessageUpdate(83002, 555, "нет"))
	if !fake.contains("Передача:") {
		t.Fatal("untyped amount error")
	}
	if telegramRequest(t, a, telegramMessageUpdate(83003, 555, "50")) != 200 {
		t.Fatal("amount")
	}
	draft := uiDraft(t, a, 83003)
	msg := uiCard(t, a, draft, telegramTestGroup)
	if _, done := a.telegramCallback(sender, telegramTestGroup, msg, "confirm:"+draft); !done {
		t.Fatal("confirm")
	}
	if !fake.contains("Ожидает главного администратора") {
		t.Fatal("pending status")
	}
	if code, out := req(t, a.confirmDraft, chief, M{"id": draft, "version": "1", "confirm_amount": "49.99"}); code != 200 {
		t.Fatal(code, out)
	}
	restarted := &App{db: a.db, storage: a.storage, location: a.location}
	if err := restarted.telegramSyncCards(10); err != nil {
		t.Fatal(err)
	}
	if !fake.contains("принято 49,99 ₽ из 50 ₽") || uiCard(t, a, draft, telegramTestGroup) != msg {
		t.Fatal("partial amount or card lost")
	}
	if fake.countMethod("sendMessage") != 2 {
		t.Fatal("appended lifecycle message")
	} // prompt + corrected error, later deleted
	assertHandoverState(t, a, draft, "posted", 0, 1)
}

func TestTelegramComfortHandoverCancellationExpiryAndIsolation(t *testing.T) {
	a, _, _, other, _, _ := handoverFixture(t)
	telegramRequest(t, a, telegramMessageUpdate(84001, 555, "/handover"))
	telegramRequest(t, a, telegramMessageUpdate(84002, 556, "50"))
	var count int
	a.db.QueryRow("SELECT count(*) FROM drafts WHERE kind='handover'").Scan(&count)
	if count != 0 {
		t.Fatal("another actor consumed dialog", other.ID)
	}
	telegramRequest(t, a, telegramMessageUpdate(84003, 555, "/cancel"))
	telegramRequest(t, a, telegramMessageUpdate(84004, 555, "50"))
	telegramRequest(t, a, telegramMessageUpdate(84005, 555, "/handover"))
	a.db.Exec("UPDATE telegram_dialogs SET expires_at=now()-interval '1 second'")
	telegramRequest(t, a, telegramMessageUpdate(84006, 555, "50"))
	a.db.QueryRow("SELECT count(*) FROM drafts WHERE kind='handover'").Scan(&count)
	if count != 0 {
		t.Fatal("expired or cancelled dialog accepted")
	}
}

func TestTelegramComfortTransferLifecycleEditsBothCards(t *testing.T) {
	a, _, sender, recipient, fake := transferFixture(t)
	telegramRequest(t, a, telegramMessageUpdateInChat(85001, 556, 556, "private", "/start"))
	draft := newTransfer(t, a, 85002, "50")
	confirmTransferSender(t, a, sender, draft)
	if err := a.deliverTransferNotifications(10); err != nil {
		t.Fatal(err)
	}
	groupMsg := uiCard(t, a, draft, telegramTestGroup)
	privateMsg := uiCard(t, a, draft, 556)
	before := fake.countMethod("sendMessage")
	if !fake.contains("Ожидает получателя · сумма зарезервирована") {
		t.Fatal("waiting")
	}
	if _, done := a.telegramRecipientTransfer(recipient, 556, 556, "receive:"+draft); !done {
		t.Fatal("receive")
	}
	if err := a.deliverTransferNotifications(10); err != nil {
		t.Fatal(err)
	}
	if before != fake.countMethod("sendMessage") || !fake.contains("50 ₽ · получено") {
		t.Fatal("final appended")
	}
	if groupMsg != uiCard(t, a, draft, telegramTestGroup) || privateMsg != uiCard(t, a, draft, 556) {
		t.Fatal("changed message ids")
	}
	assertTransferState(t, a, draft, "posted", 0, 1)
}

func TestTelegramComfortDeliveryFailureRecoversWithoutPostingAgain(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, _ = telegramFixture(t, a, card)
	telegramRequest(t, a, telegramMessageUpdate(86001, 555, "/withdraw 1234 10/0"))
	draft := uiDraft(t, a, 86001)
	var fail atomic.Bool
	fail.Store(true)
	var edits, sends atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			edits.Add(1)
			if fail.Load() {
				w.WriteHeader(503)
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sends.Add(1)
		}
		json.NewEncoder(w).Encode(M{"ok": true, "result": M{"message_id": 2000}})
	}))
	defer api.Close()
	t.Setenv("TELEGRAM_API_BASE", api.URL)
	if code := telegramRequest(t, a, telegramButtonUpdate(86002, 555, "confirm", draft)); code != 500 {
		t.Fatal("lost edit was acknowledged", code)
	}
	var entries int
	a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draft).Scan(&entries)
	if entries != 1 {
		t.Fatal("posting lost", entries)
	}
	fail.Store(false)
	restarted := &App{db: a.db, storage: a.storage, location: a.location}
	if err := restarted.telegramSyncCards(10); err != nil {
		t.Fatal(err)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(86002, 555, "confirm", draft)); code != 200 {
		t.Fatal(code)
	}
	a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draft).Scan(&entries)
	if entries != 1 || edits.Load() < 2 || sends.Load() != 0 {
		t.Fatal("delivery retried financial command or appended message", entries, edits.Load(), sends.Load())
	}
}

func TestTelegramComfortPromptDeliveryRetry(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, _ = telegramFixture(t, a, card)
	var fail atomic.Bool
	fail.Store(true)
	var sends atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Swap(false) {
			w.WriteHeader(503)
			return
		}
		sends.Add(1)
		json.NewEncoder(w).Encode(M{"ok": true, "result": M{"message_id": 2001}})
	}))
	defer api.Close()
	t.Setenv("TELEGRAM_API_BASE", api.URL)
	update := telegramMessageUpdate(87001, 555, "/withdraw")
	if telegramRequest(t, a, update) != 500 || telegramRequest(t, a, update) != 200 || telegramRequest(t, a, update) != 200 {
		t.Fatal("prompt retry")
	}
	if sends.Load() != 1 {
		t.Fatal("duplicate menu prompt", sends.Load())
	}
}

func TestTelegramComfortErrorDeliveryAndCleanupSurviveRestart(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, sender, _, _ := telegramFixture(t, a, card)
	var failSend, failDelete atomic.Bool
	failSend.Store(true)
	failDelete.Store(true)
	var sends, deletes atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if failSend.Load() {
				w.WriteHeader(503)
				return
			}
			sends.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			if failDelete.Load() {
				w.WriteHeader(503)
				return
			}
			deletes.Add(1)
		}
		json.NewEncoder(w).Encode(M{"ok": true, "result": M{"message_id": 2123}})
	}))
	defer api.Close()
	t.Setenv("TELEGRAM_API_BASE", api.URL)
	if telegramRequest(t, a, telegramMessageUpdate(88001, 555, "/withdraw 1234 нет/0")) != 500 {
		t.Fatal("failed error send acknowledged")
	}
	failSend.Store(false)
	restarted := &App{db: a.db, storage: a.storage, location: a.location}
	if err := restarted.telegramDeliverInputErrors(10); err != nil {
		t.Fatal(err)
	}
	var messageID int64
	if err := a.db.QueryRow("SELECT message_id FROM telegram_input_errors WHERE update_id=88001").Scan(&messageID); err != nil || messageID != 2123 {
		t.Fatal(messageID, err)
	}
	if err := a.telegramResolveInputErrors(telegramActor{User: sender}, telegramTestGroup, "withdraw"); err != nil {
		t.Fatal(err)
	}
	if err := a.telegramCleanupInputErrors(10); err != nil {
		t.Fatal(err)
	}
	var pending int
	a.db.QueryRow("SELECT count(*) FROM telegram_input_errors WHERE resolved AND last_error<>''").Scan(&pending)
	if pending != 1 {
		t.Fatal("delete retry lost", pending)
	}
	failDelete.Store(false)
	a.db.Exec("UPDATE telegram_input_errors SET next_attempt_at=now()")
	if err := restarted.telegramCleanupInputErrors(10); err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow("SELECT count(*) FROM telegram_input_errors").Scan(&pending)
	if pending != 0 || sends.Load() != 1 || deletes.Load() != 1 {
		t.Fatal("delivery not recovered", pending, sends.Load(), deletes.Load())
	}
}
