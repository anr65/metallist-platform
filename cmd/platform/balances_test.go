package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBalancesShowsEveryHolderAndConservesHandover(t *testing.T) {
	a := testApp(t)
	chief, _, bank, card := fixtures(t, a)
	var collectorID, chiefID string
	if err := a.db.QueryRow("SELECT id::text FROM custodians WHERE kind='collector'").Scan(&collectorID); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id::text FROM custodians WHERE kind='chief'").Scan(&chiefID); err != nil {
		t.Fatal(err)
	}
	zeroCard := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4,status) VALUES($1,$2,'Другой владелец','000000******5678','5678','retired')", zeroCard, bank); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		kind  string
		lines []Posting
	}{
		{"opening_card", []Posting{{Account: "1100", Side: "debit", Amount: 100000, Card: card}, {Account: "3100", Side: "credit", Amount: 100000}}},
		{"opening_collector", []Posting{{Account: "1200", Side: "debit", Amount: 20000, Custodian: collectorID}, {Account: "3100", Side: "credit", Amount: 20000}}},
		{"handover", []Posting{{Account: "1210", Side: "debit", Amount: 15000, Custodian: chiefID}, {Account: "1200", Side: "credit", Amount: 15000, Custodian: collectorID}}},
	} {
		if _, err = put(tx, entry.kind, id(), entry.kind+":"+id(), chief.ID, now, now, entry.lines, ""); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,90000,$3,$4,'test')", id(), card, now, chief.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.balances(w, httptest.NewRequest(http.MethodGet, "/api/balances", nil), chief)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Cards []struct {
			ID, Status, Amount, Observed string
		} `json:"cards"`
		Custodians []struct {
			Kind, Amount string
		} `json:"custodians"`
		Totals map[string]string `json:"totals"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Cards) != 2 || len(response.Custodians) != 2 || response.Totals["all"] != "1200.00" || response.Totals["cards"] != "1000.00" || response.Totals["collectors"] != "50.00" || response.Totals["chief"] != "150.00" {
		t.Fatalf("wrong balances after handover: %+v", response)
	}
	for _, item := range response.Cards {
		if item.ID == card && (item.Amount != "1000.00" || item.Observed != "900.00") {
			t.Fatalf("card expected and observed amounts: %+v", item)
		}
		if item.ID == zeroCard && (item.Amount != "0.00" || item.Status != "retired") {
			t.Fatalf("zero-balance retired card: %+v", item)
		}
	}
	w = httptest.NewRecorder()
	a.balances(w, httptest.NewRequest(http.MethodGet, "/api/balances", nil), User{ID: chief.ID, Role: "operator"})
	if w.Code != http.StatusForbidden {
		t.Fatal("operator saw all balances", w.Code)
	}
}
