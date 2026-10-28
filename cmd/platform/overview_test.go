package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func readOverview(t *testing.T, a *App, u User) M {
	t.Helper()
	w := httptest.NewRecorder()
	a.overview(w, httptest.NewRequest("GET", "/api/overview", nil), u)
	if w.Code != 200 {
		t.Fatalf("overview %d: %s", w.Code, w.Body.String())
	}
	var data M
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}
func TestOverviewEmptyAndPermissions(t *testing.T) {
	a := testApp(t)
	chief, _, _, _ := fixtures(t, a)
	for _, role := range []string{"chief", "accountant", "auditor", "sysadmin"} {
		data := readOverview(t, a, User{ID: chief.ID, Role: role})
		if data["first_day"] != nil || len(data["months"].([]interface{})) != 0 || data["totals"].(map[string]interface{})["profit"] != "0.00" {
			t.Fatal(data)
		}
	}
	for _, role := range []string{"operator", "collector"} {
		w := httptest.NewRecorder()
		a.overview(w, httptest.NewRequest("GET", "/api/overview", nil), User{Role: role})
		if w.Code != 403 {
			t.Fatalf("%s: %d", role, w.Code)
		}
	}
	w := httptest.NewRecorder()
	a.overview(w, httptest.NewRequest("POST", "/api/overview", nil), chief)
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}
func TestOverviewLedgerReversalsAndRecognition(t *testing.T) {
	a := testApp(t)
	chief, merchant, _, card := fixtures(t, a)
	originalMonth := time.Date(2026, 1, 31, 23, 59, 0, 0, a.location)
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	post := func(kind string, at time.Time, lines []Posting) string {
		t.Helper()
		entry, e := put(tx, kind, id(), id(), chief.ID, time.Now(), at, lines, "")
		if e != nil {
			t.Fatal(e)
		}
		return entry
	}
	original := post("registry", originalMonth, []Posting{{Account: "1100", Side: "debit", Amount: 10001, Card: card}, {Account: "2100", Side: "credit", Amount: 10001, Merchant: merchant}})
	post("commission", originalMonth, []Posting{{Account: "2100", Side: "debit", Amount: 400, Merchant: merchant}, {Account: "4100", Side: "credit", Amount: 400, Merchant: merchant}})
	cancelled := post("registry", originalMonth, []Posting{{Account: "1100", Side: "debit", Amount: 999, Card: card}, {Account: "2100", Side: "credit", Amount: 999, Merchant: merchant}})
	if err = reverse(tx, cancelled, chief.ID, id()); err != nil {
		t.Fatal(err)
	}
	// A later tariff adjustment belongs to January; February remains a zero month.
	post("tariff_adjustment", originalMonth, []Posting{{Account: "2100", Side: "debit", Amount: 101, Merchant: merchant}, {Account: "4100", Side: "credit", Amount: 101, Merchant: merchant}})
	post("expense", time.Date(2026, 3, 1, 0, 0, 0, 0, a.location), []Posting{{Account: "5100", Side: "debit", Amount: 123}, {Account: "1100", Side: "credit", Amount: 123, Card: card}})
	post("expense", originalMonth, []Posting{{Account: "3300", Side: "debit", Amount: 200}, {Account: "1100", Side: "credit", Amount: 200, Card: card}})
	post("other_income", originalMonth, []Posting{{Account: "1100", Side: "debit", Amount: 50, Card: card}, {Account: "4200", Side: "credit", Amount: 50}})
	post("merchant_overpayment", originalMonth, []Posting{{Account: "1300", Side: "debit", Amount: 31, Merchant: merchant}, {Account: "3100", Side: "credit", Amount: 31}})
	post("separate_merchant_payable", originalMonth, []Posting{{Account: "1100", Side: "debit", Amount: 33, Card: card}, {Account: "2110", Side: "credit", Amount: 33, Merchant: merchant}})
	for _, status := range []string{"posted", "preview", "reversed"} {
		source, rid := id(), id()
		if _, err = tx.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1::uuid,'manual','synthetic', $1::text,'application/json',0,'synthetic',$2)", source, chief.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents) VALUES($1::uuid,$2,$3,$1::text,$4,10001)", rid, merchant, source, status); err != nil {
			t.Fatal(err)
		}
		for row := 1; row <= 2; row++ {
			if _, err = tx.Exec("INSERT INTO registry_rows(id,registry_id,row_no,raw,card_id,amount_cents) VALUES($1,$2,$3,'{}',$4,5000)", id(), rid, row, card); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = tx.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1::uuid,'expense','{}',$2,$1::text)", id(), chief.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	data := readOverview(t, a, chief)
	totals := data["totals"].(map[string]interface{})
	for key, want := range map[string]string{"turnover": "100.01", "commission": "5.01", "other_revenue": "0.50", "expenses": "1.23", "profit": "4.28", "cards": "97.61", "merchant_payable": "95.33", "merchant_receivable": "0.31"} {
		if totals[key] != want {
			t.Fatalf("%s: %v, want %s", key, totals[key], want)
		}
	}
	activity := data["activity"].(map[string]interface{})
	pending := data["pending"].(map[string]interface{})
	for _, key := range []string{"registries", "merchants", "cards"} {
		if activity[key] != float64(1) {
			t.Fatal(activity)
		}
	}
	if pending["registries"] != float64(1) || pending["operations"] != float64(1) {
		t.Fatal(pending)
	}
	months := data["months"].([]interface{})
	jan := months[0].(map[string]interface{})
	feb := months[1].(map[string]interface{})
	if data["first_day"] != "2026-01-31" || jan["month"] != "2026-01" || jan["profit"] != "5.51" || feb["month"] != "2026-02" || feb["profit"] != "0.00" {
		t.Fatal(data)
	}
	// The established report and the new overview must agree on P&L and custody.
	w := httptest.NewRecorder()
	a.report(w, httptest.NewRequest("GET", "/api/report", nil), chief)
	var old M
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &old) != nil {
		t.Fatal(w.Body.String())
	}
	summary := old["summary"].(map[string]interface{})
	if summary["profit"] != totals["profit"] || summary["card_cash"] != totals["cards"] {
		t.Fatal(summary, totals)
	}
	var entries int
	if err = a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE id=$1", original).Scan(&entries); err != nil || entries != 1 {
		t.Fatal(err, entries)
	}
}
