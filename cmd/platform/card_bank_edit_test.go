package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCardBankEditValidationAuditAndHistory(t *testing.T) {
	a := testApp(t)
	chief, _, oldBank, card := fixtures(t, a)
	newBank, disabled := id(), id()
	if _, err := a.db.Exec("INSERT INTO banks(id,code,name,selectable) VALUES($1,'NEW','Новый банк',true),($2,'OFF','Недоступный банк',false)", newBank, disabled); err != nil {
		t.Fatal(err)
	}
	// A posted entry proves that editing the directory does not rewrite history.
	entry := id()
	if _, err := a.db.Exec("INSERT INTO journal_entries(id,event_type,event_id,occurred_at,recognition_at,actor_id,idempotency_key) VALUES($1,'test', $1,now(),now(),$2,$3)", entry, chief.ID, "bank-edit-"+entry); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO postings(id,entry_id,account,side,amount_cents,card_id) VALUES($1,$3,'1100','debit',10000,$4),($2,$3,'3100','credit',10000,NULL)", id(), id(), entry, card); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"collector", "accountant", "auditor"} {
		if code, _ := req(t, a.catalogUpdate, User{ID: chief.ID, Role: role}, M{"kind": "card", "id": card, "owner_label": "Тест", "bank_id": newBank}); code != 403 {
			t.Fatalf("role %s edited bank: %d", role, code)
		}
	}
	for _, value := range []interface{}{nil, "", "invalid", id(), disabled} {
		if code, out := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": card, "owner_label": "Не сохранять", "status": "blocked", "bank_id": value}); code != 400 {
			t.Fatalf("invalid bank accepted: %v %d %v", value, code, out)
		}
	}
	var bank, owner, status string
	if err := a.db.QueryRow("SELECT bank_id,owner_label,status FROM cards WHERE id=$1", card).Scan(&bank, &owner, &status); err != nil {
		t.Fatal(err)
	}
	if bank != oldBank || owner != "Тестовый владелец" || status != "active" {
		t.Fatal("rejected update changed card", bank, owner, status)
	}
	// An omitted bank preserves an old non-selectable bank for old clients.
	if _, err := a.db.Exec("UPDATE banks SET selectable=false WHERE id=$1", oldBank); err != nil {
		t.Fatal(err)
	}
	for _, body := range []M{
		{"kind": "card", "id": card, "owner_label": "Тестовый владелец"},
		{"kind": "card", "id": card, "owner_label": "Тестовый владелец", "bank_id": oldBank},
	} {
		if code, out := req(t, a.catalogUpdate, chief, body); code != 200 {
			t.Fatal(code, out)
		}
	}
	operator := User{ID: chief.ID, Role: "operator"}
	if code, out := req(t, a.catalogUpdate, operator, M{"kind": "card", "id": card, "owner_label": "Тестовый владелец", "bank_id": newBank}); code != 200 {
		t.Fatal(code, out)
	}
	if code, out := req(t, a.catalogUpdate, User{ID: chief.ID, Role: "sysadmin"}, M{"kind": "card", "id": card, "owner_label": "Тестовый владелец", "bank_id": newBank}); code != 200 {
		t.Fatal("existing sysadmin access regressed", code, out)
	}
	var mask, last4 string
	if err := a.db.QueryRow("SELECT bank_id,mask,last4 FROM cards WHERE id=$1", card).Scan(&bank, &mask, &last4); err != nil {
		t.Fatal(err)
	}
	if bank != newBank || mask != "000000******1234" || last4 != "1234" {
		t.Fatal("card identity changed", bank, mask, last4)
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='card_bank_change' AND object_id=$1 AND detail->>'before'=$2 AND detail->>'after'=$3", card, oldBank, newBank).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing bank audit", count, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM postings WHERE entry_id=$1 AND card_id=$2 AND amount_cents=10000", entry, card).Scan(&count); err != nil || count != 1 {
		t.Fatal("history changed", count, err)
	}
	w := httptest.NewRecorder()
	a.catalog(w, httptest.NewRequest(http.MethodGet, "/api/catalog", nil), chief)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"bank_id":"`+newBank+`"`) {
		t.Fatal("catalog missing bank identity", w.Code, w.Body.String())
	}
}

func TestCardBankEditDuplicateRollsBack(t *testing.T) {
	a := testApp(t)
	chief, _, oldBank, card := fixtures(t, a)
	bank := id()
	if _, err := a.db.Exec("INSERT INTO banks(id,code,name) VALUES($1,'OTHER','Другой банк')", bank); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Другой владелец','000000******1234','1234')", id(), bank); err != nil {
		t.Fatal(err)
	}
	if code, out := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": card, "owner_label": "Не сохранять", "status": "blocked", "bank_id": bank}); code != 400 || !strings.Contains(str(out, "error"), "маской") {
		t.Fatal(code, out)
	}
	var current, owner, status string
	if err := a.db.QueryRow("SELECT bank_id,owner_label,status FROM cards WHERE id=$1", card).Scan(&current, &owner, &status); err != nil {
		t.Fatal(err)
	}
	if current != oldBank || owner != "Тестовый владелец" || status != "active" {
		t.Fatal("duplicate partially updated card", current, owner, status)
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action IN ('card_bank_change','card_status_change') AND outcome='success'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rolled back change left success audit", count)
	}
}
