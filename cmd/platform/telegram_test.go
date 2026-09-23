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

func TestTelegramObservedAmount(t *testing.T) {
	tests := []struct {
		input string
		want  int64
		valid bool
	}{
		{"139к", 13_900_000, true},
		{"95k", 9_500_000, true},
		{"1,5к", 150_000, true},
		{"200", 20_000, true},
		{"0к", 0, true},
		{"0", 0, true},
		{"-1к", 0, false},
		{"92233720368548к", 0, false},
		{"abcк", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := telegramObservedAmount(tt.input)
			if (err == nil) != tt.valid || (tt.valid && got != tt.want) {
				t.Fatalf("telegramObservedAmount(%q) = %d, %v; want %d, valid=%v", tt.input, got, err, tt.want, tt.valid)
			}
		})
	}
}

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

func (f *fakeTelegram) hasCommandScope(scopeType string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if str(call, "method") != "setMyCommands" {
			continue
		}
		scope, ok := call["scope"].(map[string]interface{})
		if ok && scope["type"] == scopeType {
			return true
		}
	}
	return false
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

func TestTelegramOperatorUsesAllActiveCardsWithFieldRestrictions(t *testing.T) {
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	admin, _, _, fake := telegramFixture(t, a, card)
	operator := User{ID: id(), Login: "field-operator", Name: "Операционист", Role: "operator"}
	operatorCustodian := id()
	if _, e := a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,$2,'operator')", operatorCustodian, operator.Name); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,$2,$3,'operator','x')", operator.ID, operator.Login, operator.Name); e != nil {
		t.Fatal(e)
	}
	if status, _ := req(t, a.linkTelegram, admin, M{"user_id": operator.ID, "custodian_id": operatorCustodian, "telegram_id": "557"}); status != 200 {
		t.Fatal("operator Telegram link failed", status)
	}
	tx, e := a.tx()
	if e != nil {
		t.Fatal(e)
	}
	_, e = put(tx, "test_funding", id(), "tg-operator-funding-"+id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 1_000_000, Card: card}, {Account: "3100", Side: "credit", Amount: 1_000_000}}, "")
	if e != nil || tx.Commit() != nil {
		t.Fatal("funding failed", e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(1051, 557, "/expense 1234 зарплата 230")); code != 200 {
		t.Fatal(code)
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key='telegram:1051'").Scan(&count); e != nil || count != 0 || !fake.contains("только расходы «Прогрев» и «Банк. Комиссия»") {
		t.Fatal("operator created forbidden expense", count, e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(1052, 557, "/withdraw 1234 1000/0")); code != 200 {
		t.Fatal(code)
	}
	var draftID string
	if e := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:1052'").Scan(&draftID); e != nil {
		t.Fatal(e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(1053, 557, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	var cash int64
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account='1200' AND custodian_id=$1", operatorCustodian).Scan(&cash); e != nil || cash != 100_000 {
		t.Fatal("operator cash balance wrong", cash, e)
	}
}

func TestTelegramCollectorWithdrawalBatchIsAtomicIdempotentAndReversible(t *testing.T) {
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	_, collector, custodian, fake := telegramFixture(t, a, card)
	tx, e := a.tx()
	if e != nil {
		t.Fatal(e)
	}
	_, e = put(tx, "test_funding", id(), "tg-withdrawal-batch-funding-"+id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 20_000_000, Card: card}, {Account: "3100", Side: "credit", Amount: 20_000_000}}, "")
	if e != nil || tx.Commit() != nil {
		t.Fatal("funding failed", e)
	}
	message := "/withdraw 1234 100к/139к\n1234 50к/95к"
	if code := telegramRequest(t, a, telegramMessageUpdate(1101, 555, message)); code != 200 {
		t.Fatal(code)
	}
	if !fake.contains("Подтвердите снятия и остатки по картам") || !fake.contains("1. ****1234 · снято 100 000 ₽ · остаток 139 000 ₽") || !fake.contains("2. ****1234 · снято 50 000 ₽ · остаток 95 000 ₽") || !fake.contains("Итого снято: 150 000 ₽") {
		t.Fatal("withdrawal batch preview is incomplete")
	}
	var draftID, status string
	if e := a.db.QueryRow("SELECT id,status FROM drafts WHERE idempotency_key='telegram:1101'").Scan(&draftID, &status); e != nil || status != "draft" {
		t.Fatal("withdrawal batch draft missing", status, e)
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draftID).Scan(&count); e != nil || count != 0 {
		t.Fatal("withdrawal preview changed balances", count, e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(1102, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(1103, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='withdrawal' AND event_id=$1", draftID).Scan(&count); e != nil || count != 1 {
		t.Fatal("withdrawal batch duplicated or missing", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM postings p JOIN journal_entries j ON p.entry_id=j.id WHERE j.event_id=$1", draftID).Scan(&count); e != nil || count != 4 {
		t.Fatal("withdrawal batch postings count is wrong", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM observations WHERE source=$1 AND reporter_id=$2", "telegram:"+draftID, collector.ID).Scan(&count); e != nil || count != 2 {
		t.Fatal("withdrawal observations duplicated or missing", count, e)
	}
	var latestObserved int64
	if e := a.db.QueryRow("SELECT observed_cents FROM observations WHERE source=$1 ORDER BY observed_at DESC,id DESC LIMIT 1", "telegram:"+draftID).Scan(&latestObserved); e != nil || latestObserved != 9_500_000 {
		t.Fatal("latest withdrawal observation is wrong", latestObserved, e)
	}
	var cash, cardBalance int64
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN account='1200' AND custodian_id=$1 AND side='debit' THEN amount_cents WHEN account='1200' AND custodian_id=$1 AND side='credit' THEN -amount_cents ELSE 0 END),0),COALESCE(SUM(CASE WHEN account='1100' AND card_id=$2 AND side='debit' THEN amount_cents WHEN account='1100' AND card_id=$2 AND side='credit' THEN -amount_cents ELSE 0 END),0) FROM postings", custodian, card).Scan(&cash, &cardBalance); e != nil || cash != 15_000_000 || cardBalance != 5_000_000 {
		t.Fatal("withdrawal batch balances are wrong", cash, cardBalance, e)
	}
	if !fake.contains("Снятия подтверждены: 2 снятия на 150 000 ₽") {
		t.Fatal("withdrawal batch completion message missing")
	}

	// Leave enough cash for every individual line but not for their total. The reversal must still be rejected.
	tx, _ = a.tx()
	spendEntry, e := put(tx, "test_spend", id(), "tg-withdrawal-batch-spend-"+id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "5100", Side: "debit", Amount: 4_000_000, Category: "warmup"}, {Account: "1200", Side: "credit", Amount: 4_000_000, Custodian: custodian}}, "")
	if e != nil || tx.Commit() != nil {
		t.Fatal("test spend failed", e)
	}
	if code, _ := req(t, a.reverseDraft, chief, M{"id": draftID, "reason": "проверка совокупного остатка"}); code != 409 {
		t.Fatal("batch reversal ignored aggregate cash balance", code)
	}
	tx, _ = a.tx()
	if e = reverse(tx, spendEntry, chief.ID, "reverse-test-spend-"+id()); e != nil || tx.Commit() != nil {
		t.Fatal("test spend reversal failed", e)
	}
	if code, out := req(t, a.reverseDraft, chief, M{"id": draftID, "reason": "исправление пакетного снятия"}); code != 200 {
		t.Fatal("withdrawal batch reversal failed", code, out)
	}
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN account='1200' AND custodian_id=$1 AND side='debit' THEN amount_cents WHEN account='1200' AND custodian_id=$1 AND side='credit' THEN -amount_cents ELSE 0 END),0),COALESCE(SUM(CASE WHEN account='1100' AND card_id=$2 AND side='debit' THEN amount_cents WHEN account='1100' AND card_id=$2 AND side='credit' THEN -amount_cents ELSE 0 END),0) FROM postings", custodian, card).Scan(&cash, &cardBalance); e != nil || cash != 0 || cardBalance != 20_000_000 {
		t.Fatal("withdrawal batch reversal did not restore balances", cash, cardBalance, e)
	}
}

func TestTelegramWithdrawalBatchRejectsInvalidAndAllowsNegativeCardBalance(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	if code := telegramRequest(t, a, telegramMessageUpdate(1201, 555, "/withdraw 1234 10/20\n1234 неверно")); code != 200 {
		t.Fatal(code)
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key='telegram:1201'").Scan(&count); e != nil || count != 0 || !fake.contains("Строка 2") {
		t.Fatal("invalid withdrawal item did not reject whole batch", count, e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(1202, 555, "/withdraw 1234 600/100\n1234 500/50")); code != 200 {
		t.Fatal(code)
	}
	var draftID string
	if e := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:1202'").Scan(&draftID); e != nil {
		t.Fatal(e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(1203, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(1204, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draftID).Scan(&count); e != nil || count != 1 {
		t.Fatal("zero-balance withdrawal batch was not posted exactly once", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM observations WHERE source=$1", "telegram:"+draftID).Scan(&count); e != nil || count != 2 {
		t.Fatal("withdrawal observations missing or duplicated", count, e)
	}
	var balance int64
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account='1100' AND card_id=$1", card).Scan(&balance); e != nil || balance != -110_000 {
		t.Fatal("withdrawal did not produce expected negative card balance", balance, e)
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

func TestTelegramCollectorExpenseBatchIsAtomicAndIdempotent(t *testing.T) {
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	tx, _ := a.tx()
	_, e := put(tx, "test_funding", id(), "tg-expense-batch-funding-"+id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 100_000, Card: card}, {Account: "3100", Side: "credit", Amount: 100_000}}, "")
	if e != nil || tx.Commit() != nil {
		t.Fatal(e)
	}
	message := "/expense 1234 прогрев 230\n1234 банк. комиссия 25,50"
	if code := telegramRequest(t, a, telegramMessageUpdate(2101, 555, message)); code != 200 {
		t.Fatal(code)
	}
	if !fake.contains("Подтвердите расходы по картам") || !fake.contains("1. ****1234 · Прогрев · 230 ₽") || !fake.contains("2. ****1234 · Банк. Комиссия · 25,50 ₽") || !fake.contains("Итого: 255,50 ₽") {
		t.Fatal("batch preview is incomplete")
	}
	var draftID, status string
	if e := a.db.QueryRow("SELECT id,status FROM drafts WHERE idempotency_key='telegram:2101'").Scan(&draftID, &status); e != nil || status != "draft" {
		t.Fatal("batch draft missing", status, e)
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draftID).Scan(&count); e != nil || count != 0 {
		t.Fatal("preview changed balances", count, e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(2102, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(2103, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='expense' AND event_id=$1", draftID).Scan(&count); e != nil || count != 1 {
		t.Fatal("batch journal duplicated or missing", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM postings p JOIN journal_entries j ON p.entry_id=j.id WHERE j.event_id=$1", draftID).Scan(&count); e != nil || count != 4 {
		t.Fatal("batch postings count is wrong", count, e)
	}
	var warmup, bankFee, cardCredit int64
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN p.account='5100' AND p.category='warmup' AND p.side='debit' THEN p.amount_cents ELSE 0 END),0),COALESCE(SUM(CASE WHEN p.account='5300' AND p.category='bank_fee' AND p.side='debit' THEN p.amount_cents ELSE 0 END),0),COALESCE(SUM(CASE WHEN p.account='1100' AND p.card_id=$2 AND p.side='credit' THEN p.amount_cents ELSE 0 END),0) FROM postings p JOIN journal_entries j ON p.entry_id=j.id WHERE j.event_id=$1", draftID, card).Scan(&warmup, &bankFee, &cardCredit); e != nil || warmup != 23_000 || bankFee != 2_550 || cardCredit != 25_550 {
		t.Fatal("batch amounts are wrong", warmup, bankFee, cardCredit, e)
	}
	if !fake.contains("Расходы подтверждены: 2 расхода на 255,50 ₽") {
		t.Fatal("batch completion message missing")
	}
	if code, out := req(t, a.reverseDraft, chief, M{"id": draftID, "reason": "исправление пакетного расхода"}); code != 200 {
		t.Fatal("batch reversal failed", code, out)
	}
	var cardBalance, expenseBalance int64
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN account='1100' AND card_id=$1 AND side='debit' THEN amount_cents WHEN account='1100' AND card_id=$1 AND side='credit' THEN -amount_cents ELSE 0 END),0),COALESCE(SUM(CASE WHEN account IN ('5100','5300') AND side='debit' THEN amount_cents WHEN account IN ('5100','5300') AND side='credit' THEN -amount_cents ELSE 0 END),0) FROM postings", card).Scan(&cardBalance, &expenseBalance); e != nil || cardBalance != 100_000 || expenseBalance != 0 {
		t.Fatal("batch reversal did not restore balances", cardBalance, expenseBalance, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE reversal_of=(SELECT id FROM journal_entries WHERE event_id=$1 AND event_type='expense')", draftID).Scan(&count); e != nil || count != 1 {
		t.Fatal("batch reversal missing or duplicated", count, e)
	}
}

func TestTelegramExpenseBatchRejectsInvalidAndAllowsNegativeCardBalance(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	if code := telegramRequest(t, a, telegramMessageUpdate(2201, 555, "/expense 1234 прогрев 10; 1234 зарплата 20")); code != 200 {
		t.Fatal(code)
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key='telegram:2201'").Scan(&count); e != nil || count != 0 || !fake.contains("Строка 2") {
		t.Fatal("invalid item did not reject whole batch", count, e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(2202, 555, "/expense 1234 прогрев 600; 1234 банк. комиссия 500")); code != 200 {
		t.Fatal(code)
	}
	var draftID string
	if e := a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:2202'").Scan(&draftID); e != nil {
		t.Fatal(e)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(2203, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if code := telegramRequest(t, a, telegramButtonUpdate(2204, 555, "confirm", draftID)); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draftID).Scan(&count); e != nil || count != 1 {
		t.Fatal("zero-balance expense batch was not posted exactly once", count, e)
	}
	var status string
	if e := a.db.QueryRow("SELECT status FROM drafts WHERE id=$1", draftID).Scan(&status); e != nil || status != "posted" {
		t.Fatal("confirmed expense batch status is wrong", status, e)
	}
	var balance int64
	if e := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account='1100' AND card_id=$1", card).Scan(&balance); e != nil || balance != -110_000 {
		t.Fatal("expense did not produce expected negative card balance", balance, e)
	}
}

func TestTelegramCardCollisionAndSensitiveInput(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	bank, second := id(), id()
	if _, e := a.db.Exec("INSERT INTO banks(id,code,name) VALUES($1,'SECOND','Учебный банк 2')", bank); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Вымышленный владелец','999999******1234','1234')", second, bank); e != nil {
		t.Fatal(e)
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

func TestTelegramCancelStopsPendingInputWithoutDraftOrPosting(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, _, fake := telegramFixture(t, a, card)
	if code := telegramRequest(t, a, telegramMessageUpdate(5001, 555, "/expense")); code != 200 {
		t.Fatal(code)
	}
	var count int
	if e := a.db.QueryRow("SELECT count(*) FROM telegram_dialogs WHERE user_id=$1 AND command='expense'", collector.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("expense input was not started", count, e)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(5002, 555, "/cancel@metallist_test_bot")); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM telegram_dialogs WHERE user_id=$1", collector.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("cancel left pending input", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM drafts WHERE created_by=$1", collector.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("cancel created a draft", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM postings").Scan(&count); e != nil || count != 0 {
		t.Fatal("cancel created postings", count, e)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='input_cancel' AND outcome='success'", collector.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("cancel audit missing", count, e)
	}
	if !fake.contains("Ввод отменён. Обычные сообщения больше не обрабатываются.") {
		t.Fatal("cancel acknowledgement missing")
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(5003, 555, "обычное сообщение после отмены")); code != 200 {
		t.Fatal(code)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM telegram_updates WHERE update_id=5003").Scan(&count); e != nil || count != 0 {
		t.Fatal("ordinary message after cancel was processed", count, e)
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
	if call == nil || !fake.hasCommandScope("chat") || !fake.hasCommandScope("all_group_chats") {
		t.Fatal("group command menus have wrong scopes", call)
	}
	commands, ok := call["commands"].([]interface{})
	if !ok || len(commands) != 4 {
		t.Fatal("group command menu has wrong size", call)
	}
	names := make([]string, 0, len(commands))
	for _, raw := range commands {
		command, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatal("group command menu is malformed", call)
		}
		names = append(names, str(M(command), "command"))
	}
	if strings.Join(names, ",") != "start,expense,withdraw,cancel" {
		t.Fatal("group command menu contains unexpected commands", names)
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
