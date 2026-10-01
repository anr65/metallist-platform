package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMerchantCatalogDebtMatchesLedger(t *testing.T) {
	a := testApp(t)
	chief, merchant, _, card := fixtures(t, a)
	zero, inactive := id(), id()
	if _, err := a.db.Exec("INSERT INTO merchants(id,code,name,active) VALUES($1,'ZERO','Без операций',true),($2,'OLD','Отключённый',false)", zero, inactive); err != nil {
		t.Fatal(err)
	}
	post := func(key string, lines []Posting) {
		t.Helper()
		tx, err := a.tx()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = put(tx, "test", id(), key, chief.ID, time.Now(), time.Now(), lines, ""); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	post("funding", []Posting{{Account: "1100", Side: "debit", Amount: 10001, Card: card}, {Account: "2100", Side: "credit", Amount: 9601, Merchant: merchant}, {Account: "4100", Side: "credit", Amount: 400, Merchant: merchant}})
	post("partial-repayment", []Posting{{Account: "2100", Side: "debit", Amount: 2500, Merchant: merchant}, {Account: "1100", Side: "credit", Amount: 2500, Card: card}})
	post("correction", []Posting{{Account: "2100", Side: "debit", Amount: 101, Merchant: merchant}, {Account: "4100", Side: "credit", Amount: 101, Merchant: merchant}})
	post("reversal", []Posting{{Account: "2100", Side: "credit", Amount: 101, Merchant: merchant}, {Account: "4100", Side: "debit", Amount: 101, Merchant: merchant}})
	post("inactive-funding", []Posting{{Account: "1100", Side: "debit", Amount: 999, Card: card}, {Account: "2100", Side: "credit", Amount: 999, Merchant: inactive}})
	// A proposed repayment does not affect the published balance.
	if code, out := req(t, a.draft, chief, M{"kind": "repayment", "merchant_id": merchant, "amount": "10.00", "card_id": card}); code != 201 {
		t.Fatal(code, out)
	}
	read := func(role string) []M {
		t.Helper()
		w := httptest.NewRecorder()
		a.catalog(w, httptest.NewRequest(http.MethodGet, "/api/catalog", nil), User{ID: chief.ID, Role: role})
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Merchants []M `json:"merchants"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Merchants
	}
	expected := map[string]string{merchant: "71.01", zero: "0.00", inactive: "9.99"}
	var total int64
	for _, m := range read("chief") {
		merchantID := m["id"].(string)
		if m["payable"] != expected[merchantID] {
			t.Fatalf("unexpected debt: %v", m)
		}
		tx, err := a.tx()
		if err != nil {
			t.Fatal(err)
		}
		cents, err := balance(tx, "2100", "merchant", merchantID)
		tx.Rollback()
		if err != nil || m["payable"] != rub(cents) {
			t.Fatal("debt disagrees with ledger", m, err)
		}
		total += cents
	}
	if total != 8100 {
		t.Fatal("wrong total", total)
	}
	for _, role := range []string{"operator", "accountant", "auditor", "sysadmin"} {
		for _, m := range read(role) {
			if _, exists := m["payable"]; exists {
				t.Fatal("debt exposed to", role)
			}
		}
	}
	if len(read("collector")) != 0 {
		t.Fatal("collector saw merchants")
	}
}
