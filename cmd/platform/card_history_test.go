package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func historyRequest(t *testing.T, a *App, u User, query string) M {
	t.Helper()
	w := httptest.NewRecorder()
	a.cardHistory(w, httptest.NewRequest("GET", "/api/card-history?"+query, nil), u)
	if w.Code != 200 {
		t.Fatalf("history %d: %s", w.Code, w.Body.String())
	}
	var result M
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCardHistoryAccessAndValidation(t *testing.T) {
	a := testApp(t)
	chief, _, _, _ := fixtures(t, a)
	for _, role := range []string{"operator", "collector", ""} {
		w := httptest.NewRecorder()
		a.cardHistory(w, httptest.NewRequest("GET", "/?card_id="+id(), nil), User{ID: chief.ID, Role: role})
		if w.Code != 403 {
			t.Fatalf("%s: %d", role, w.Code)
		}
	}
	for _, query := range []string{"", "card_id=1234", "card_id=" + id() + "&scope=invalid", "card_id=" + id() + "&page=0", "card_id=" + id() + "&page=oops", "card_id=" + id() + "&date_from=invalid", "card_id=" + id() + "&date_from=2026-02-01&date_to=2026-01-01"} {
		w := httptest.NewRecorder()
		a.cardHistory(w, httptest.NewRequest("GET", "/?"+query, nil), User{Role: "chief"})
		if w.Code != 400 {
			t.Fatalf("%s: %d", query, w.Code)
		}
	}
	w := httptest.NewRecorder()
	a.cardHistory(w, httptest.NewRequest("POST", "/", nil), User{Role: "chief"})
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}

func TestCardHistoryLedgerSourcesAndPagination(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, card := fixtures(t, a)
	other := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4,status) VALUES($1,$2,'Другой владелец','111111******1234','1234','retired')", other, bank); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, a.location)
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	source, rid := id(), id()
	if _, err = tx.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1::uuid,'manual','synthetic',$1::text,'application/json',0,'synthetic',$2)", source, chief.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,payment_date) VALUES($1,$2,$3,'SYNTHETIC','posted',10001,'2026-01-02')", rid, merchant, source); err != nil {
		t.Fatal(err)
	}
	for row := 1; row <= 2; row++ {
		if _, err = tx.Exec("INSERT INTO registry_rows(id,registry_id,row_no,raw,card_id,amount_cents,rrn,external_order) VALUES($1,$2,$3,'{}',$4,$5,'TEST-RRN','TEST-ORDER')", id(), rid, row, card, int64(5000+row-1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = put(tx, "registry", rid, id(), chief.ID, day, day, []Posting{{Account: "1100", Side: "debit", Amount: 10001, Card: card}, {Account: "2100", Side: "credit", Amount: 10001, Merchant: merchant}}, "")
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := id()
	if _, err = tx.Exec("INSERT INTO drafts(id,kind,payload,status,created_by,idempotency_key) VALUES($1::uuid,'withdrawal',jsonb_build_object('card_id',$2::text,'amount_cents',2000),'posted',$3,$1::text)", withdrawal, card, chief.ID); err != nil {
		t.Fatal(err)
	}
	var custodian string
	if err = tx.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&custodian); err != nil {
		t.Fatal(err)
	}
	original, err := put(tx, "withdrawal", withdrawal, id(), chief.ID, day.Add(24*time.Hour), day, []Posting{{Account: "1100", Side: "credit", Amount: 2000, Card: card}, {Account: "1200", Side: "debit", Amount: 2000, Custodian: custodian}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = reverse(tx, original, chief.ID, id()); err != nil {
		t.Fatal(err)
	}
	// Same last four digits must never pull the other card into this history.
	if _, err = put(tx, "opening", id(), id(), chief.ID, day, day, []Posting{{Account: "1100", Side: "debit", Amount: 999, Card: other}, {Account: "3100", Side: "credit", Amount: 999}}, ""); err != nil {
		t.Fatal(err)
	}
	// An unrelated expense from cash must stay out too.
	if _, err = put(tx, "expense", id(), id(), chief.ID, day, day, []Posting{{Account: "5900", Side: "debit", Amount: 100}, {Account: "1200", Side: "credit", Amount: 100, Custodian: custodian}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1::uuid,'withdrawal',jsonb_build_object('withdrawal_items',jsonb_build_array(jsonb_build_object('card_id',$2::text,'amount_cents',123),jsonb_build_object('card_id',$3::text,'amount_cents',999)),'amount_cents',1122),$4,$1::text)", id(), card, other, chief.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,777,$3,$4,'web')", id(), card, day, chief.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"chief", "accountant", "auditor", "sysadmin"} {
		result := historyRequest(t, a, User{ID: chief.ID, Role: role}, "card_id="+card)
		if result["balance"] != "100.01" || len(result["items"].([]any)) != 5 {
			t.Fatal(result)
		}
	}
	ledger := historyRequest(t, a, chief, "card_id="+card+"&scope=ledger")
	foundReversal := false
	for _, raw := range ledger["items"].([]any) {
		item := raw.(map[string]any)
		switch item["kind"] {
		case "registry":
			if item["income"] != "100.01" || item["balance"] != "100.01" || len(item["details"].(map[string]any)["rows"].([]any)) != 2 {
				t.Fatal(item)
			}
		case "withdrawal":
			if item["expense"] != "20.00" || item["balance"] != "80.01" || item["status"] != "reversed" {
				t.Fatal(item)
			}
		case "reversal":
			foundReversal = true
			if item["income"] != "20.00" || item["balance"] != "100.01" || item["document_id"] != withdrawal {
				t.Fatal(item)
			}
		default:
			t.Fatal(item)
		}
	}
	if !foundReversal {
		t.Fatal("missing reversal")
	}
	drafts := historyRequest(t, a, chief, "card_id="+card+"&scope=draft")
	if drafts["items"].([]any)[0].(map[string]any)["amount"] != "1.23" {
		t.Fatal(drafts)
	}
	filtered := historyRequest(t, a, chief, "card_id="+card+"&scope=ledger&date_from=2026-01-03&date_to=2026-01-03")
	rows := filtered["items"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["balance"] != "80.01" {
		t.Fatal(filtered)
	}
	// The Moscow boundary is Jan 2 although UTC is still Jan 1.
	observed := historyRequest(t, a, chief, "card_id="+card+"&scope=observation&date_from=2026-01-02&date_to=2026-01-02")
	if len(observed["items"].([]any)) != 1 || observed["balance"] != "100.01" {
		t.Fatal(observed)
	}
	for i := 0; i < 16; i++ {
		if _, err = a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,0,$3,$4,'web')", id(), card, day, chief.ID); err != nil {
			t.Fatal(err)
		}
	}
	first := historyRequest(t, a, chief, "card_id="+card+"&scope=observation")
	second := historyRequest(t, a, chief, "card_id="+card+"&scope=observation&page=2")
	if len(first["items"].([]any)) != 15 || first["has_more"] != true || len(second["items"].([]any)) != 2 || second["has_more"] != false {
		t.Fatal(first, second)
	}
	seen := map[string]bool{}
	for _, page := range []M{first, second} {
		for _, raw := range page["items"].([]any) {
			key := raw.(map[string]any)["id"].(string)
			if seen[key] {
				t.Fatal("duplicate page row")
			}
			seen[key] = true
		}
	}
	empty := historyRequest(t, a, chief, "card_id="+other+"&scope=observation")
	if len(empty["items"].([]any)) != 0 || empty["balance"] != "9.99" {
		t.Fatal(empty)
	}
	w := httptest.NewRecorder()
	a.cardHistory(w, httptest.NewRequest("GET", "/?card_id="+id(), nil), chief)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	encoded, _ := json.Marshal(ledger)
	if strings.Contains(string(encoded), "raw\"") || strings.Contains(string(encoded), "0000000000061234") {
		t.Fatal("sensitive source data exposed")
	}
}

func TestCardHistorySharedCardsAndUnpostedEvidence(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, physical := fixtures(t, a)
	virtual := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4,physical_card_id) VALUES($1,$2,'Тест','000000******5678','5678',$3)", virtual, bank, physical); err != nil {
		t.Fatal(err)
	}
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	day := time.Date(2026, 2, 1, 0, 0, 0, 0, a.location)
	if _, err = put(tx, "opening", id(), id(), chief.ID, day, day, []Posting{{Account: "1100", Side: "debit", Amount: 150000, Card: virtual}, {Account: "3100", Side: "credit", Amount: 150000}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = put(tx, "expense", id(), id(), chief.ID, day.Add(time.Hour), day, []Posting{{Account: "1100", Side: "credit", Amount: 40000, Card: physical}, {Account: "5300", Side: "debit", Amount: 40000, Category: "bank_fee"}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,109900,$3,$4,'web')", id(), virtual, day, chief.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"preview", "deleted"} {
		source, rid := id(), id()
		if _, err = tx.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1::uuid,'manual','synthetic',$1::text,'application/json',0,'synthetic',$2)", source, chief.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,deleted_at,deleted_by) VALUES($1::uuid,$2,$3,$1::text,$4,123,CASE WHEN $4='deleted' THEN now() END,CASE WHEN $4='deleted' THEN $5::uuid END)", rid, merchant, source, status, chief.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO registry_rows(id,registry_id,row_no,raw,card_id,amount_cents,rrn,external_order) VALUES($1,$2,1,'{}',$3,123,'RRN-SYNTHETIC','ORDER-SYNTHETIC')", id(), rid, virtual); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec("INSERT INTO drafts(id,kind,payload,status,created_by,idempotency_key) VALUES($1::uuid,'expense',jsonb_build_object('expense_items',jsonb_build_array(jsonb_build_object('card_id',$2::text,'amount_cents',456,'category','bank_fee')),'amount_cents',456),'rejected',$3,$1::text)", id(), virtual, chief.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, card := range []string{physical, virtual} {
		all := historyRequest(t, a, chief, "card_id="+card)
		if all["balance"] != "1100.00" || all["card"].(map[string]any)["shared"] != true || len(all["items"].([]any)) != 6 {
			t.Fatal(all)
		}
		evidence := historyRequest(t, a, chief, "card_id="+card+"&scope=registry")
		if len(evidence["items"].([]any)) != 2 {
			t.Fatal(evidence)
		}
		for _, raw := range evidence["items"].([]any) {
			item := raw.(map[string]any)
			if item["income"] != nil || item["balance"] != nil || item["amount"] != "1.23" {
				t.Fatal(item)
			}
		}
		drafts := historyRequest(t, a, chief, "card_id="+card+"&scope=draft")
		item := drafts["items"].([]any)[0].(map[string]any)
		if item["amount"] != "4.56" || item["status"] != "rejected" || item["balance"] != nil {
			t.Fatal(item)
		}
		ledger := historyRequest(t, a, chief, "card_id="+card+"&scope=ledger")
		entries := ledger["items"].([]any)
		if len(entries) != 2 || entries[0].(map[string]any)["balance"] != "1100.00" || entries[1].(map[string]any)["balance"] != "1500.00" {
			t.Fatal(ledger)
		}
	}
}
