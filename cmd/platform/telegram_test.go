package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTelegram struct {
	mu    sync.Mutex
	calls []M
}

const telegramTestGroup int64 = -100777000111

func telegramFixture(t *testing.T, a *App, card string) (User, User, string, *fakeTelegram) {
	t.Helper()
	fake := &fakeTelegram{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body M
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid Telegram API body")
		}
		body["method"] = strings.TrimPrefix(r.URL.Path, "/botsynthetic:token/")
		fake.mu.Lock()
		fake.calls = append(fake.calls, body)
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(server.Close)
	file := t.TempDir() + "/telegram-token"
	if e := os.WriteFile(file, []byte("synthetic:token"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", file)
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "synthetic-secret")
	t.Setenv("TELEGRAM_API_BASE", server.URL)
	t.Setenv("TELEGRAM_ALLOWED_CHAT_ID", "-100777000111")
	admin := User{ID: id(), Login: "sysadmin", Role: "sysadmin"}
	collector := User{ID: id(), Login: "collector", Role: "collector"}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'sysadmin','Admin','sysadmin','x'),($2,'collector','Сборщик','collector','x')", admin.ID, collector.ID); e != nil {
		t.Fatal(e)
	}
	var custodian string
	if e := a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&custodian); e != nil {
		t.Fatal(e)
	}
	if status, _ := req(t, a.assignCard, admin, M{"user_id": collector.ID, "card_id": card}); status != 200 {
		t.Fatal("card assignment failed", status)
	}
	if status, _ := req(t, a.linkTelegram, admin, M{"user_id": collector.ID, "custodian_id": custodian, "telegram_id": "555"}); status != 200 {
		t.Fatal("Telegram link failed", status)
	}
	return admin, collector, custodian, fake
}

func telegramRequest(t *testing.T, a *App, update M) int {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(string(encode(update))))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "synthetic-secret")
	a.telegram(w, r)
	return w.Code
}

func telegramMessageUpdate(updateID int64, telegramID int64, text string) M {
	return telegramMessageUpdateInChat(updateID, telegramID, telegramTestGroup, "supergroup", text)
}

func telegramMessageUpdateInChat(updateID, telegramID, chatID int64, chatType, text string) M {
	return M{"update_id": updateID, "message": M{"message_id": updateID, "text": text, "chat": M{"id": chatID, "type": chatType}, "from": M{"id": telegramID}}}
}

func telegramButtonUpdate(updateID int64, telegramID int64, action, draftID string) M {
	return M{"update_id": updateID, "callback_query": M{"id": "synthetic-callback", "data": action + ":" + draftID, "from": M{"id": telegramID}, "message": M{"message_id": int64(500), "chat": M{"id": telegramTestGroup, "type": "supergroup"}}}}
}

func (f *fakeTelegram) contains(text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if strings.Contains(str(call, "text"), text) {
			return true
		}
	}
	return false
}

func (f *fakeTelegram) called(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if str(call, "method") == method {
			return true
		}
	}
	return false
}

func (f *fakeTelegram) call(method string) M {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if str(call, "method") == method {
			return call
		}
	}
	return nil
}

func TestTelegramCollectorWithdrawalConfirmationAndRetry(t *testing.T) {
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	_, collector, custodian, fake := telegramFixture(t, a, card)
	tx, e := a.tx()
	if e != nil {
		t.Fatal(e)
	}
	_, e = put(tx, "test_funding", id(), "tg-funding-"+id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 10_050_000, Card: card}, {Account: "3100", Side: "credit", Amount: 10_050_000}}, "")
	if e != nil || tx.Commit() != nil {
		t.Fatal("funding failed", e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(1001, 555, "/withdraw@metallist_test_bot 1234 100к/200")); code != 200 {
		t.Fatal(code)
	}
	if !fake.contains("Автор: Сборщик") || !fake.contains("Карта: ****1234") || !fake.contains("Сумма снятия: 100 000 ₽") || !fake.contains("Остаток: 200 ₽") {
		t.Fatal("preview missing exact input")
	}
	var draftID, status string
	if e := a.db.QueryRow("SELECT id,status FROM drafts WHERE idempotency_key='telegram:1001'").Scan(&draftID, &status); e != nil || status != "draft" {
		t.Fatal("preview created money", status, e)
	}
	if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "100000.00"}); code != 409 {
		t.Fatal("chief bypassed sender confirmation", code)
	}
	otherCustodian, otherUser := id(), id()
	if _, e := a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,'Другой сборщик','collector')", otherCustodian); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash,telegram_id,custodian_id) VALUES($1,'other-collector','Другой','collector','x',556,$2)", otherUser, otherCustodian); e != nil {
		t.Fatal(e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(1002, 556, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	var before int
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draftID).Scan(&before); e != nil || before != 0 {
		t.Fatal("another collector confirmed withdrawal", before, e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(1003, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(1004, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='withdrawal' AND event_id=$1", draftID).Scan(&count); e != nil || count != 1 {
		t.Fatal("withdrawal posted twice or missing", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM observations WHERE source=$1 AND observed_cents=20000 AND reporter_id=$2", "telegram:"+draftID, collector.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("observation duplicated or missing", count, e)
	}
	var cash, cardBalance int64
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account='1200' AND custodian_id=$1", custodian).Scan(&cash); e != nil || cash != 10_000_000 {
		t.Fatal("cash credited to wrong person", cash, e)
	}
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account='1100' AND card_id=$1", card).Scan(&cardBalance); e != nil || cardBalance != 50_000 {
		t.Fatal("card balance wrong", cardBalance, e)
	}
}

func TestTelegramCollectorExpensePermissionsAndRejection(t *testing.T) {
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	tx, _ := a.tx()
	_, e := put(tx, "test_funding", id(), "tg-expense-funding-"+id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 100_000, Card: card}, {Account: "3100", Side: "credit", Amount: 100_000}}, "")
	if e != nil || tx.Commit() != nil {
		t.Fatal(e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(2001, 555, "/expense 1234 зарплата 230")); code != 200 {
		t.Fatal(code)
	}
	var updateMetadata string
	if e := a.db.QueryRow("SELECT raw_update::text FROM telegram_updates WHERE update_id=2001").Scan(&updateMetadata); e != nil || strings.Contains(updateMetadata, "зарплата") {
		t.Fatal("arbitrary Telegram text persisted", e)
	}
	var count int
	_ = a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key='telegram:2001'").Scan(&count)
	if count != 0 || !fake.contains("только расходы «Прогрев» и «Банк. Комиссия»") {
		t.Fatal("collector created forbidden expense")
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(2002, 555, "/expense 1234 прогрев 230")); code != 200 {
		t.Fatal(code)
	}
	var draftID string
	if e := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:2002'").Scan(&draftID); e != nil || !fake.contains("Категория: Прогрев") {
		t.Fatal("expense preview missing", e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(2003, 555, "reject", draftID)); code != 200 {
		t.Fatal(code)
	}
	_ = a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draftID).Scan(&count)
	if count != 0 {
		t.Fatal("rejected expense posted")
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(2004, 555, "/expense")); code != 200 || !fake.contains("Введите последние 4 цифры карты") {
		t.Fatal("command menu follow-up missing")
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(2005, 555, "1234 банк. комиссия 25,50")); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:2005'").Scan(&draftID); e != nil || !fake.contains("Категория: Банк. Комиссия") {
		t.Fatal("bank fee preview missing", e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(2006, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM postings p JOIN journal_entries j ON p.entry_id=j.id WHERE j.event_id=$1 AND p.account='5300' AND p.side='debit' AND p.amount_cents=2550", draftID).Scan(&count); e != nil || count != 1 {
		t.Fatal("bank fee posted incorrectly", count, e)
	}
}

func TestTelegramCardCollisionAndSensitiveInput(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	admin, collector, _, fake := telegramFixture(t, a, card)
	bank, second := id(), id()
	if _, e := a.db.Exec("INSERT INTO banks(id,code,name) VALUES($1,'SECOND','Учебный банк 2')", bank); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Вымышленный владелец','999999******1234','1234')", second, bank); e != nil {
		t.Fatal(e)
	}
	if status, _ := req(t, a.assignCard, admin, M{"user_id": collector.ID, "card_id": second}); status != 200 {
		t.Fatal("second card not assigned", status)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(3001, 555, "/withdraw 1234 100к/200")); code != 200 {
		t.Fatal(code)
	}
	if !fake.contains("неоднозначны") {
		t.Fatal("collision was not rejected")
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key='telegram:3001'").Scan(&count); e != nil || count != 0 {
		t.Fatal("ambiguous card created draft", count, e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(3002, 555, "/withdraw 1234567812341234 100к/200")); code != 200 {
		t.Fatal(code)
	}
	var raw string
	if e := a.db.QueryRow("SELECT raw_update::text FROM telegram_updates WHERE update_id=3002").Scan(&raw); e != nil || strings.Contains(raw, "1234567812341234") || !fake.contains("Не отправляйте полный номер карты") {
		t.Fatal("full card number persisted or not rejected", e)
	}
	if !fake.called("deleteMessage") {
		t.Fatal("sensitive group message was not deleted")
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(3003, 555, "/withdraw 1234 5678 9012 3456 100к/200")); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT raw_update::text FROM telegram_updates WHERE update_id=3003").Scan(&raw); e != nil || strings.Contains(raw, "5678 9012") || !strings.Contains(raw, "redacted") {
		t.Fatal("grouped card number persisted", e)
	}
}

func TestTelegramOnlyAllowsConfiguredGroup(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	if code := telegramRequest(t, a, telegramMessageUpdateInChat(4001, 555, 555, "private", "/withdraw 1234 1/0")); code != 200 {
		t.Fatal(code)
	}
	if code := telegramRequest(t, a, telegramMessageUpdateInChat(4002, 555, -100999, "supergroup", "/withdraw 1234 1/0")); code != 200 {
		t.Fatal(code)
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key IN ('telegram:4001','telegram:4002')").Scan(&count); e != nil || count != 0 {
		t.Fatal("command outside configured group created a draft", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM telegram_updates WHERE update_id IN (4001,4002)").Scan(&count); e != nil || count != 0 {
		t.Fatal("command outside configured group was persisted", count, e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdateInChat(4003, 555, -100999, "supergroup", "/start@metallist_test_bot")); code != 200 || !fake.contains("Telegram chat ID: -100999") {
		t.Fatal("group bootstrap did not disclose its chat ID", code)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(4004, 555, "обычное сообщение в рабочей группе")); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM telegram_updates WHERE update_id=4004").Scan(&count); e != nil || count != 0 {
		t.Fatal("ordinary group conversation was persisted", count, e)
	}
}

func TestTelegramRegistersWebhookAndGroupMenu(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	t.Setenv("TELEGRAM_WEBHOOK_URL", "https://example.test/telegram/webhook")
	a.telegramSetupCommands()
	if !fake.called("setWebhook") {
		t.Fatal("webhook was not registered")
	}
	call := fake.call("setMyCommands")
	scope, ok := call["scope"].(map[string]interface{})
	if call == nil || !ok || scope["type"] != "chat" || scope["chat_id"] != float64(telegramTestGroup) {
		t.Fatal("group command menu has wrong scope", call)
	}
}

func TestCollectorWebAccessIsRestricted(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, _, _ := telegramFixture(t, a, card)
	for _, check := range []struct {
		name string
		fn   handler
		url  string
	}{
		{"report", a.report, "/api/report"},
		{"registries", a.registries, "/api/registries"},
		{"registry rows", a.registryRows, "/api/registry/rows"},
		{"drafts", a.drafts, "/api/drafts"},
	} {
		w := httptest.NewRecorder()
		check.fn(w, httptest.NewRequest(http.MethodGet, check.url, nil), collector)
		if w.Code != 403 {
			t.Fatal("collector accessed", check.name, w.Code)
		}
	}
	if status, _ := req(t, a.draft, collector, M{"kind": "expense", "category": "salary", "amount": "1.00"}); status != 403 {
		t.Fatal("collector created web expense", status)
	}
	w := httptest.NewRecorder()
	a.catalog(w, httptest.NewRequest(http.MethodGet, "/api/catalog", nil), collector)
	var catalog M
	if e := json.Unmarshal(w.Body.Bytes(), &catalog); e != nil || w.Code != 200 || catalog["users"] != nil || catalog["merchants"] != nil || len(catalog["cards"].([]interface{})) != 1 {
		t.Fatal("collector catalog leaked unrelated data", w.Code, e)
	}
}
