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
		Totals map[string]any `json:"totals"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Cards) != 2 || len(response.Custodians) != 2 || response.Totals["all"] != "1200.00" || response.Totals["cards"] != "1000.00" || response.Totals["collectors"] != "50.00" || response.Totals["chief"] != "150.00" {
		t.Fatalf("wrong balances after handover: %+v", response)
	}
	if response.Totals["cards_observed"] != "900.00" || response.Totals["cards_observed_count"] != float64(1) {
		t.Fatalf("reported balance must not change ledger totals: %+v", response.Totals)
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

func TestBalancesSumsLatestObservationPerCard(t *testing.T) {
	a := testApp(t)
	chief, _, bank, card := fixtures(t, a)
	secondCard, missingCard := id(), id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4,status) VALUES($1,$3,'Владелец 2','000000******5678','5678','retired'),($2,$3,'Владелец 3','000000******9012','9012','active')", secondCard, missingCard, bank); err != nil {
		t.Fatal(err)
	}
	check := func(want any, count float64) {
		t.Helper()
		w := httptest.NewRecorder()
		a.balances(w, httptest.NewRequest(http.MethodGet, "/api/balances", nil), chief)
		var result struct {
			Totals map[string]any `json:"totals"`
		}
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Totals["cards_observed"] != want || result.Totals["cards_observed_count"] != count || result.Totals["cards"] != "0.00" || result.Totals["all"] != "0.00" {
			t.Fatalf("incorrect observation totals: %+v", result.Totals)
		}
	}
	check(nil, 0)
	now := time.Now().UTC()
	// A real reported zero must remain distinguishable from no data.
	if _, err := a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,0,$3,$4,'test')", id(), card, now, chief.ID); err != nil {
		t.Fatal(err)
	}
	check("0.00", 1)
	// Late insertion of an older report must not replace the latest report.
	for _, observation := range []struct {
		card  string
		cents int64
		at    time.Time
	}{
		{card, 12345, now.Add(time.Hour)},
		{card, 99999, now.Add(-time.Hour)},
		{secondCard, -234, now},
	} {
		if _, err := a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,$3,$4,$5,'test')", id(), observation.card, observation.cents, observation.at, chief.ID); err != nil {
			t.Fatal(err)
		}
	}
	check("121.11", 2)
}
