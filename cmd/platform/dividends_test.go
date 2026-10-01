package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDividendPayoutBalancesAndProfit(t *testing.T) {
	for _, sourceKind := range []string{"card", "chief", "collector"} {
		t.Run(sourceKind, func(t *testing.T) {
			a := testApp(t)
			chief, _, _, card := fixtures(t, a)
			sourceID, kind := card, "card"
			if sourceKind != "card" {
				kind = "cash"
				if err := a.db.QueryRow("SELECT id FROM custodians WHERE kind=$1", sourceKind).Scan(&sourceID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := a.tx()
			if err != nil {
				t.Fatal(err)
			}
			source, err := moneySource(tx, kind, sourceID)
			if err != nil {
				t.Fatal(err)
			}
			source.Side, source.Amount = "debit", 112345
			if _, err = put(tx, "test_funding", id(), id(), chief.ID, time.Now(), time.Now(), []Posting{source, {Account: "3100", Side: "credit", Amount: 100000}, {Account: "4100", Side: "credit", Amount: 12345}}, ""); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			readReport := func() M {
				w := httptest.NewRecorder()
				a.report(w, httptest.NewRequest("GET", "/api/report", nil), chief)
				var report M
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
					t.Fatal(err)
				}
				return report["summary"].(map[string]interface{})
			}
			check := func(cash, equity string) {
				t.Helper()
				s := readReport()
				for key, value := range map[string]string{"card_cash": cash, "equity": equity, "profit": "123.45", "expenses": "0.00", "merchant_payable": "0.00", "balance_difference": "0.00"} {
					if s[key] != value {
						t.Fatalf("%s: got %v want %s", key, s[key], value)
					}
				}
				monthly, err := a.monthReport(time.Now().In(a.location).Format("2006-01"))
				if err != nil || monthly["profit"] != "123.45" || monthly["expenses"] != "0.00" {
					t.Fatal("monthly P&L changed", monthly, err)
				}
			}
			check("1123.45", "1000.00")
			code, created := req(t, a.draft, chief, M{"kind": "expense", "category": "dividends", "source_kind": kind, "source_id": sourceID, "amount": "30.01"})
			if code != 201 {
				t.Fatal(code, created)
			}
			draftID := created["id"].(string)
			check("1123.45", "1000.00") // Drafts do not move money.
			if code, _ := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "30.00"}); code != 409 {
				t.Fatal("changed amount accepted", code)
			}
			for i := 0; i < 2; i++ {
				if code, out := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "30.01"}); code != 200 {
					t.Fatal(code, out)
				}
			}
			check("1093.44", "969.99")
			var count int
			if err := a.db.QueryRow("SELECT COUNT(*) FROM postings p JOIN journal_entries j ON j.id=p.entry_id WHERE j.event_id=$1 AND p.account='3300' AND p.side='debit' AND p.category='dividends' AND p.amount_cents=3001", draftID).Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicate or incorrect payout", count, err)
			}
			var sourceCredit int64
			if err := a.db.QueryRow("SELECT COALESCE(SUM(p.amount_cents),0) FROM postings p JOIN journal_entries j ON j.id=p.entry_id WHERE j.event_id=$1 AND p.account=$2 AND p.side='credit' AND (p.card_id=$3 OR p.custodian_id=$3)", draftID, source.Account, sourceID).Scan(&sourceCredit); err != nil || sourceCredit != 3001 {
				t.Fatal("wrong source deduction", sourceCredit, err)
			}
			code, overdrawn := req(t, a.draft, chief, M{"kind": "expense", "category": "dividends", "source_kind": kind, "source_id": sourceID, "amount": "1093.45"})
			if code != 201 {
				t.Fatal(code, overdrawn)
			}
			if code, _ := req(t, a.confirmDraft, chief, M{"id": overdrawn["id"], "version": "1", "confirm_amount": "1093.45"}); code != 409 {
				t.Fatal("overdraft accepted", code)
			}
			check("1093.44", "969.99")
			if code, out := req(t, a.reverseDraft, chief, M{"id": draftID, "reason": "Исправление тестовой выплаты"}); code != 200 {
				t.Fatal(code, out)
			}
			check("1123.45", "1000.00")
			var net int64
			if err := a.db.QueryRow("SELECT SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) FROM postings").Scan(&net); err != nil || net != 0 {
				t.Fatal("unbalanced ledger", net, err)
			}
			for _, role := range []string{"operator", "collector", "accountant", "sysadmin"} {
				if code, _ := req(t, a.draft, User{Role: role}, M{"kind": "expense", "category": "dividends", "amount": "1.00"}); code != 403 {
					t.Fatal("unauthorized payout", role, code)
				}
			}
		})
	}
}
