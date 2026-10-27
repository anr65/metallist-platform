package main

import (
	"database/sql"
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

func accountingFixture(t *testing.T) (*App, User, User, string, string, *fakeTelegram) {
	t.Helper()
	a := testApp(t)
	chief, merchant, _, card := fixtures(t, a)
	admin, _, _, fake := telegramFixture(t, a, card)
	if code, out := req(t, a.linkTelegram, admin, M{"user_id": admin.ID, "telegram_id": "777"}); code != 200 {
		t.Fatal(code, out)
	}
	if code := telegramRequest(t, a, telegramMessageUpdateInChat(20001, 777, 777, "private", "/start")); code != 200 {
		t.Fatal("start", code)
	}
	return a, chief, admin, merchant, card, fake
}
func accountingRegistry(t *testing.T, a *App, u User, merchant, card string) string {
	t.Helper()
	contact := id()
	if _, err := a.db.Exec("INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,'Синтетический контакт','+79990001122',$2)", contact, u.ID); err != nil {
		t.Fatal(err)
	}
	code, out := req(t, a.createManualRegistry, u, M{"merchant_id": merchant, "payment_date": testPaymentDate(), "idempotency_key": id(), "rows": []M{{"card_id": card, "contact_id": contact, "amount": "1000.00"}}})
	if code != 201 {
		t.Fatal("manual", code, out)
	}
	return out["id"].(string)
}
func accountingToken(t *testing.T, a *App, rid, uid string) string {
	t.Helper()
	if err := a.deliverAccountingNotifications(20); err != nil {
		t.Fatal("deliver", err)
	}
	var token string
	if err := a.db.QueryRow(`SELECT p.id FROM accounting_registry_previews p JOIN accounting_notifications n ON n.id=p.notification_id WHERE n.object_id=$1 AND n.user_id=$2 ORDER BY p.created_at DESC LIMIT 1`, rid, uid).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return token
}
func TestAccountingRegistryStalePreviewAndIdempotentConfirmation(t *testing.T) {
	a, _, admin, merchant, card, fake := accountingFixture(t)
	rid := accountingRegistry(t, a, admin, merchant, card)
	token := accountingToken(t, a, rid, admin.ID)
	if !fake.contains("фактическое поступление денег") {
		t.Fatal("no explicit confirmation")
	}
	if _, err := a.db.Exec("UPDATE tariffs SET rate_bp=500 WHERE merchant_id=$1", merchant); err != nil {
		t.Fatal(err)
	}
	actor := telegramActor{User: admin}
	if msg := a.accountingRegistryCallback(actor, 777, 777, token); !strings.Contains(msg, "Данные изменились") {
		t.Fatal(msg)
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM journal_entries").Scan(&count); err != nil || count != 0 {
		t.Fatal("stale posted", count, err)
	}
	next := accountingToken(t, a, rid, admin.ID)
	if next == token {
		t.Fatal("no new preview")
	}
	if msg := a.accountingRegistryCallback(actor, 778, 778, next); strings.Contains(msg, "подтвержден.") {
		t.Fatal("foreign actor accepted", msg)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.accountingRegistryCallback(actor, 777, 777, next) }()
	}
	wg.Wait()
	if msg := a.accountingRegistryCallback(actor, 777, 777, next); msg != "Реестр уже обработан" {
		t.Fatal("retry", msg)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='registry' AND event_id=$1", rid).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate", count, err)
	}
	var commission int64
	if err := a.db.QueryRow("SELECT commission_cents FROM registries WHERE id=$1", rid).Scan(&commission); err != nil || commission != 5000 {
		t.Fatal("wrong commission", commission, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE actor_id=$1 AND channel='telegram' AND action='registry_confirm'", admin.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("audit", count, err)
	}
}
func TestAccountingRegistryRevocationAndMissingData(t *testing.T) {
	a, _, admin, merchant, card, _ := accountingFixture(t)
	rid := accountingRegistry(t, a, admin, merchant, card)
	token := accountingToken(t, a, rid, admin.ID)
	if _, err := a.db.Exec("UPDATE registry_rows SET error_code='missing_card' WHERE registry_id=$1", rid); err != nil {
		t.Fatal(err)
	}
	if msg := a.accountingRegistryCallback(telegramActor{User: admin}, 777, 777, token); !strings.Contains(msg, "Данные изменились") {
		t.Fatal(msg)
	}
	if err := a.deliverAccountingNotifications(10); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE users SET active=false WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if msg := a.accountingRegistryCallback(telegramActor{User: admin}, 777, 777, token); !strings.Contains(msg, "Доступ изменился") {
		t.Fatal(msg)
	}
}
func TestAccountingNotificationsRetryAndAtomicity(t *testing.T) {
	a, _, admin, merchant, card, _ := accountingFixture(t)
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if err = queueAccountingNotification(tx, "registry", id()); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var count int
	if err = a.db.QueryRow("SELECT count(*) FROM accounting_notifications").Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback outbox", count, err)
	}
	rid := accountingRegistry(t, a, admin, merchant, card)
	base := os.Getenv("TELEGRAM_API_BASE")
	t.Setenv("TELEGRAM_API_BASE", "http://127.0.0.1:1")
	if err = a.deliverAccountingNotifications(10); err != nil {
		t.Fatal(err)
	}
	var state, last string
	if err = a.db.QueryRow("SELECT state,last_error FROM accounting_notifications WHERE object_id=$1", rid).Scan(&state, &last); err != nil || state != "pending" || last == "" {
		t.Fatal(state, last, err)
	}
	t.Setenv("TELEGRAM_API_BASE", base)
	if _, err = a.db.Exec("UPDATE accounting_notifications SET next_attempt_at=now()"); err != nil {
		t.Fatal(err)
	}
	restarted := &App{db: a.db, storage: a.storage, location: a.location}
	if err = restarted.deliverAccountingNotifications(10); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow("SELECT state FROM accounting_notifications WHERE object_id=$1", rid).Scan(&state); err != nil || state != "sent" {
		t.Fatal(state, err)
	}
}
func TestAccountingBalancesAndFullSysadminPermissions(t *testing.T) {
	a, chief, admin, merchant, card, fake := accountingFixture(t)
	if _, err := a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,0,now(),$3,'test')", id(), card, chief.ID); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"cards", "merchants", "custodians", "summary"} {
		if err := a.accountingBalances(telegramActor{User: admin}, 777, 777, section, 0); err != nil {
			t.Fatal(section, err)
		}
	}
	if !fake.contains("Факт: 0,00") || !fake.contains("Данные по 1 из 1 карт") {
		t.Fatal("missing factual zero/coverage")
	}
	w := httptest.NewRecorder()
	a.balances(w, httptest.NewRequest("GET", "/api/balances", nil), admin)
	if w.Code != 200 {
		t.Fatal("sysadmin read", w.Code)
	}
	accountingRegistry(t, a, admin, merchant, card)
	if _, err := a.db.Exec("UPDATE users SET telegram_id=778 WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	before := len(fake.calls)
	if err := a.accountingBalances(telegramActor{User: admin}, 777, 777, "cards", 0); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != before+1 || !fake.contains("Доступ изменился") {
		t.Fatal("old binding allowed")
	}
}
func TestAccountingHandoverAndRepaymentNotifications(t *testing.T) {
	a, chief, admin, merchant, _, fake := accountingFixture(t)
	var from, to string
	if err := a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&from); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id FROM custodians WHERE kind='chief'").Scan(&to); err != nil {
		t.Fatal(err)
	}
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = put(tx, "opening", id(), id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1200", Side: "debit", Amount: 10000, Custodian: from}, {Account: "3100", Side: "credit", Amount: 10000}}, ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, input := range []M{{"kind": "handover", "from_custodian_id": from, "to_custodian_id": to, "amount": "50"}, {"kind": "repayment", "merchant_id": merchant, "source_kind": "cash", "source_id": to, "amount": "20"}} {
		code, out := req(t, a.draft, admin, input)
		if code != 201 {
			t.Fatal("draft", code, out)
		}
		var n int
		if err = a.db.QueryRow("SELECT count(*) FROM accounting_notifications WHERE object_id=$1", out["id"]).Scan(&n); err != nil || n != 0 {
			t.Fatal("draft notified", n, err)
		}
		if code, out = req(t, a.confirmDraft, admin, M{"id": out["id"], "version": "1", "confirm_amount": input["amount"]}); code != 200 {
			t.Fatal("post", code, out)
		}
	}
	if err = a.deliverAccountingNotifications(20); err != nil {
		t.Fatal(err)
	}
	if !fake.contains("Передача главному администратору проведена") || !fake.contains("Возврат мерчанту проведен") {
		t.Fatal("missing posted event")
	}
}
func TestAccountingWithProductionPrivileges(t *testing.T) {
	a, _, admin, merchant, card, _ := accountingFixture(t)
	rid := accountingRegistry(t, a, admin, merchant, card)
	const role = "metallist_accounting_limited_test"
	_, err := a.db.Exec(`CREATE ROLE metallist_accounting_limited_test;
 GRANT USAGE ON SCHEMA public TO metallist_accounting_limited_test;
 GRANT SELECT ON ALL TABLES IN SCHEMA public TO metallist_accounting_limited_test;
 GRANT INSERT,UPDATE ON accounting_notifications TO metallist_accounting_limited_test;
 GRANT INSERT ON accounting_registry_previews,audit_events,journal_entries,postings TO metallist_accounting_limited_test;
 GRANT UPDATE(telegram_id,custodian_id) ON users TO metallist_accounting_limited_test;
 GRANT UPDATE(status,commission_cents,rate_bp,confirmed_at) ON registries TO metallist_accounting_limited_test;`)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["role"] = role
	db := sql.OpenDB(stdlib.GetConnector(*cfg))
	t.Cleanup(func() { db.Close(); a.db.Exec("DROP OWNED BY " + role + "; DROP ROLE " + role) })
	limited := &App{db: db, storage: a.storage, location: a.location}
	token := accountingToken(t, limited, rid, admin.ID)
	if msg := limited.accountingRegistryCallback(telegramActor{User: admin}, 777, 777, token); !strings.Contains(msg, "Реестр подтвержден") {
		t.Fatal(msg)
	}
	// Ensure no universal role workaround leaked into collector APIs.
	w := httptest.NewRecorder()
	if limited.require(w, User{Role: "collector"}, "chief") {
		t.Fatal("collector privileged")
	}
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
}

func TestAccountingCardPaginationMissingFactAndIdenticalMasks(t *testing.T) {
	a, chief, admin, _, card, fake := accountingFixture(t)
	var bank string
	if err := a.db.QueryRow("SELECT bank_id FROM cards WHERE id=$1", card).Scan(&bank); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		bank = id()
		if _, err := a.db.Exec("INSERT INTO banks(id,code,name) VALUES($1,$2,'Другой банк')", bank, bank); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4,status) VALUES($1,$2,'Синтетика','000000******1234','1234','retired')", id(), bank); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,-123,now(),$3,'test')", id(), card, chief.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.accountingBalances(telegramActor{User: admin}, 777, 777, "cards", 1); err != nil {
		t.Fatal(err)
	}
	if !fake.contains("2/2") || !fake.contains("Факт: нет данных") {
		t.Fatal("no second page/missing fact")
	}
	if err := a.accountingBalances(telegramActor{User: admin}, 777, 777, "summary", 0); err != nil {
		t.Fatal(err)
	}
	if !fake.contains("Данные по 1 из 17 карт") || !fake.contains("-1,23") {
		t.Fatal("partial/negative observations lost")
	}
}
func TestAccountingWebAndBotRaceUsesSamePostingCommand(t *testing.T) {
	a, chief, admin, merchant, card, _ := accountingFixture(t)
	rid := accountingRegistry(t, a, admin, merchant, card)
	tok := accountingToken(t, a, rid, admin.ID)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.accountingRegistryCallback(telegramActor{User: admin}, 777, 777, tok) }()
	go func() {
		defer wg.Done()
		tx, e := a.tx()
		if e != nil {
			return
		}
		defer tx.Rollback()
		_, _, e = a.confirmRegistryCommand(tx, chief, M{"id": rid, "version": "1", "confirm_total": "1000.00", "confirm_commission": "40.00", "confirm_rate_bp": "400"}, "web")
		if e == nil {
			tx.Commit()
		}
	}()
	wg.Wait()
	var n int
	if err := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='registry' AND event_id=$1", rid).Scan(&n); err != nil || n != 1 {
		t.Fatal("race duplicated", n, err)
	}
	tx, e := a.tx()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var payable int64
	if e = tx.QueryRow("SELECT sum(CASE WHEN side='credit' THEN amount_cents ELSE -amount_cents END) FROM postings WHERE account='2100' AND merchant_id=$1", merchant).Scan(&payable); e != nil || payable != 96000 {
		t.Fatal("wrong debt", payable, e)
	}
}
