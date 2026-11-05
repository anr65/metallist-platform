package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func createVirtualTestCard(t *testing.T, a *App, chief User, bank, physical, pan string) string {
	t.Helper()
	var handler string
	if err := a.db.QueryRow("SELECT id FROM drop_handlers LIMIT 1").Scan(&handler); err != nil {
		t.Fatal(err)
	}
	code, out := req(t, a.catalogCreate, chief, M{"kind": "card", "bank_id": bank, "owner_label": "Виртуальная тестовая", "pan": pan, "drop_handler_id": handler, "physical_card_id": physical})
	if code != 201 {
		t.Fatalf("virtual create: %d %v", code, out)
	}
	return out["id"].(string)
}

func TestVirtualCardRegistryWithdrawalAndReversal(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, physical := fixtures(t, a)
	virtual := createVirtualTestCard(t, a, chief, bank, physical, "4111111111111111")
	other := createVirtualTestCard(t, a, chief, bank, physical, "4242424242424242")
	var cust string
	if err := a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&cust); err != nil {
		t.Fatal(err)
	}
	contact := id()
	if _, err := a.db.Exec("INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,'Тестовый Получатель','+79990000001',$2)", contact, chief.ID); err != nil {
		t.Fatal(err)
	}
	code, out := req(t, a.createManualRegistry, chief, M{"merchant_id": merchant, "payment_date": testPaymentDate(), "idempotency_key": id(), "rows": []M{{"card_id": virtual, "contact_id": contact, "amount": "1000.00"}, {"card_id": other, "contact_id": contact, "amount": "500.00"}}})
	if code != 201 {
		t.Fatalf("registry: %d %v", code, out)
	}
	registry := out["id"].(string)
	confirm := M{"id": registry, "version": "1", "confirm_total": "1500.00", "confirm_commission": "60.00", "confirm_rate_bp": "400"}
	for i := 0; i < 2; i++ {
		if code, out := req(t, a.confirmRegistry, chief, confirm); code != 200 {
			t.Fatalf("confirm/retry: %d %v", code, out)
		}
	}
	check := func(want string) {
		t.Helper()
		tx, err := a.tx()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for _, card := range []string{physical, virtual, other} {
			v, err := balance(tx, "1100", "card", card)
			if err != nil || rub(v) != want {
				t.Fatalf("shared balance: %s %s %v", card, rub(v), err)
			}
		}
	}
	check("1500.00")
	code, out = req(t, a.draft, chief, M{"kind": "withdrawal", "card_id": physical, "custodian_id": cust, "amount": "400.00"})
	if code != 201 {
		t.Fatalf("withdrawal draft: %d %v", code, out)
	}
	withdrawal := out["id"].(string)
	for i := 0; i < 2; i++ {
		if code, out := req(t, a.confirmDraft, chief, M{"id": withdrawal, "version": "1", "confirm_amount": "400.00"}); code != 200 {
			t.Fatalf("withdrawal: %d %v", code, out)
		}
	}
	check("1100.00")
	// Expense through either instrument consumes the same available money.
	code, out = req(t, a.draft, chief, M{"kind": "expense", "source_kind": "card", "source_id": virtual, "category": "bank_fee", "amount": "10.00"})
	if code != 201 {
		t.Fatalf("expense draft: %d %v", code, out)
	}
	expense := out["id"].(string)
	if code, out := req(t, a.confirmDraft, chief, M{"id": expense, "version": "1", "confirm_amount": "10.00"}); code != 200 {
		t.Fatalf("expense: %d %v", code, out)
	}
	check("1090.00")
	// All cards show one latest observation; global totals count the group once.
	if code, out := req(t, a.observation, chief, M{"card_id": physical, "amount": "1090.00"}); code != 201 {
		t.Fatalf("observation: %d %v", code, out)
	}
	w := httptest.NewRecorder()
	a.balances(w, httptest.NewRequest("GET", "/api/balances", nil), chief)
	var data struct {
		Cards  []M
		Totals M
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || w.Code != 200 {
		t.Fatalf("balances: %d %s %v", w.Code, w.Body.String(), err)
	}
	if len(data.Cards) != 3 || data.Totals["cards"] != "1090.00" || data.Totals["cards_observed"] != "1090.00" || data.Totals["all"] != "1490.00" {
		t.Fatalf("duplicated totals: %+v", data)
	}
	for _, card := range data.Cards {
		if card["amount"] != "1090.00" || card["observed"] != "1090.00" {
			t.Fatalf("unsynchronized card: %v", card)
		}
	}

	// Insufficient balance is checked against the group, including the other virtual card.
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.eventLines(tx, "withdrawal", M{"card_id": physical, "custodian_id": cust}, 109001); err == nil {
		t.Fatal("shared overdraft accepted")
	}
	if _, err = a.eventLines(tx, "withdrawal", M{"card_id": virtual, "custodian_id": cust}, 100); err == nil {
		t.Fatal("ATM withdrawal on virtual accepted")
	}
	tx.Rollback()
	if code, out := req(t, a.reverseDraft, chief, M{"id": withdrawal, "reason": "Тест сторно"}); code != 200 {
		t.Fatalf("reverse: %d %v", code, out)
	}
	check("1490.00")
	if code, out := req(t, a.reverseDraft, chief, M{"id": expense, "reason": "Тест сторно"}); code != 200 {
		t.Fatalf("reverse expense: %d %v", code, out)
	}
	check("1500.00")
	var entries, cardDebits, cardCredits int
	if err := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='registry' AND event_id=$1", registry).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("registry retry duplicated: %d %v", entries, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FILTER(WHERE side='debit'),count(*) FILTER(WHERE side='credit') FROM postings WHERE account='1100'").Scan(&cardDebits, &cardCredits); err != nil || cardDebits != 4 || cardCredits != 2 {
		t.Fatalf("unexpected postings: %d %d %v", cardDebits, cardCredits, err)
	}
	var unbalanced int
	if err := a.db.QueryRow("SELECT count(*) FROM (SELECT entry_id FROM postings GROUP BY entry_id HAVING sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END)<>0) x").Scan(&unbalanced); err != nil || unbalanced != 0 {
		t.Fatalf("unbalanced entries: %d %v", unbalanced, err)
	}
}

func TestVirtualCardLinkGuards(t *testing.T) {
	a := testApp(t)
	chief, _, bank, physical := fixtures(t, a)
	virtual := createVirtualTestCard(t, a, chief, bank, physical, "4111111111111111")
	edit := func(card, parent string) int {
		code, _ := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": card, "owner_label": "Тестовая карта", "physical_card_id": parent})
		return code
	}
	if edit(virtual, virtual) != 400 || edit(physical, virtual) != 400 || edit(virtual, id()) != 400 {
		t.Fatal("invalid topology accepted")
	}
	// Unused card can be detached and attached again, with auditable transitions.
	if edit(virtual, "") != 200 || edit(virtual, physical) != 200 {
		t.Fatal("unused link change failed")
	}
	if code, _ := req(t, a.catalogUpdate, User{ID: chief.ID, Role: "collector"}, M{"kind": "card", "id": virtual, "owner_label": "Тест", "physical_card_id": ""}); code != 403 {
		t.Fatal("collector edited link")
	}
	if _, err := a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,0,now(),$3,'test')", id(), virtual, chief.ID); err != nil {
		t.Fatal(err)
	}
	if edit(virtual, "") != 400 {
		t.Fatal("used link changed")
	}
	if _, err := a.db.Exec("UPDATE cards SET physical_card_id=NULL WHERE id=$1", virtual); err == nil {
		t.Fatal("SQL bypassed history guard")
	}
	if _, err := a.db.Exec("UPDATE cards SET physical_card_id=$2 WHERE id=$1", physical, virtual); err == nil {
		t.Fatal("SQL bypassed topology guard")
	}
	// Editing descriptive data and resubmitting the same link stays valid.
	if edit(virtual, physical) != 200 {
		t.Fatal("unchanged used link rejected")
	}
	var audits int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='card_balance_link_change' AND object_id=$1", virtual).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("link audit: %d %v", audits, err)
	}
}

func TestVirtualCardCannotLinkDraftOrRegistryHistory(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, physical := fixtures(t, a)
	virtual := createVirtualTestCard(t, a, chief, bank, physical, "4111111111111111")
	// Even a Telegram batch draft freezes identity before any postings exist.
	if _, err := a.db.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,'withdrawal',jsonb_build_object('withdrawal_items',jsonb_build_array(jsonb_build_object('card_id',$2::text))),$3,$4)", id(), virtual, chief.ID, id()); err != nil {
		t.Fatal(err)
	}
	if code, _ := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": virtual, "owner_label": "Тест", "physical_card_id": ""}); code != 400 {
		t.Fatal("batch draft link changed")
	}
	// A physical card already issued to a merchant cannot become virtual either.
	request := id()
	if _, err := a.db.Exec("INSERT INTO payment_requests(id,merchant_id,external_ref,mode,payment_count,created_by) VALUES($1,$2,'virtual-test','cards',1,$3)", request, merchant, chief.ID); err != nil {
		t.Fatal(err)
	}
	fresh := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Тест','000000******8888','8888')", fresh, bank); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO payment_request_rows(id,request_id,row_no,card_id) VALUES($1,$2,1,$3)", id(), request, fresh); err != nil {
		t.Fatal(err)
	}
	if code, _ := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": fresh, "owner_label": "Тест", "physical_card_id": physical}); code != 400 {
		t.Fatal("request history link changed")
	}
}

func TestVirtualCardTelegramSharedObservation(t *testing.T) {
	a := testApp(t)
	chief, _, bank, physical := fixtures(t, a)
	virtual := createVirtualTestCard(t, a, chief, bank, physical, "4111111111111111")
	createVirtualTestCard(t, a, chief, bank, physical, "4242424242424242")
	_, collector, _, fake := telegramFixture(t, a, physical)
	if code, _ := req(t, a.observation, chief, M{"card_id": virtual, "amount": "123.45"}); code != 201 {
		t.Fatal("observation failed")
	}
	if err := a.telegramCardsBalance(telegramActor{User: collector}, telegramTestGroup, 1); err != nil {
		t.Fatal(err)
	}
	text := str(fake.calls[len(fake.calls)-1], "text")
	if !strings.Contains(text, "Общий фактический баланс: "+telegramMoney(12345)) || strings.Count(text, telegramMoney(12345)) != 4 {
		t.Fatalf("duplicated Telegram balance: %s", text)
	}
}

func TestVirtualCardPrivateAccountingBalances(t *testing.T) {
	a, chief, admin, _, physical, fake := accountingFixture(t)
	var bank string
	if err := a.db.QueryRow("SELECT bank_id FROM cards WHERE id=$1", physical).Scan(&bank); err != nil {
		t.Fatal(err)
	}
	virtual := createVirtualTestCard(t, a, chief, bank, physical, "4111111111111111")
	createVirtualTestCard(t, a, chief, bank, physical, "4242424242424242")
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	_, err = put(tx, "test_virtual", id(), id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 12345, Card: virtual}, {Account: "3100", Side: "credit", Amount: 12345}}, "")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if code, _ := req(t, a.observation, chief, M{"card_id": virtual, "amount": "123.45"}); code != 201 {
		t.Fatal("observation")
	}
	if err = a.accountingBalances(telegramActor{User: admin}, 777, 777, "cards", 0); err != nil {
		t.Fatal(err)
	}
	text := str(fake.calls[len(fake.calls)-1], "text")
	if strings.Count(text, "Учетный: "+accountingMoney(12345)) != 3 || strings.Count(text, "Факт: "+accountingMoney(12345)) != 3 {
		t.Fatal(text)
	}
	if err = a.accountingBalances(telegramActor{User: admin}, 777, 777, "summary", 0); err != nil {
		t.Fatal(err)
	}
	text = str(fake.calls[len(fake.calls)-1], "text")
	if !strings.Contains(text, "Карты, учетный: "+accountingMoney(12345)) || !strings.Contains(text, "Карты, последние фактические: "+accountingMoney(12345)) || !strings.Contains(text, "Данные по 1 из 1 карт") {
		t.Fatal(text)
	}
}
