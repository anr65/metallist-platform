package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTelegramSalaryParsing(t *testing.T) {
	for _, input := range []string{"", "50000", "0 comment", "-1 comment", "1.001 comment", "92233720368548к comment", "abc comment"} {
		if _, err := telegramSalary(input, "author"); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	p, err := telegramSalary("50000,01  За сентябрь; премия\nИван", "author")
	if err != nil || p["amount_cents"] != int64(5000001) || p["source_id"] != "author" || p["comment"] != "За сентябрь; премия\nИван" {
		t.Fatal(p, err)
	}
}

func TestTelegramSalaryCash(t *testing.T) {
	for _, role := range []string{"collector", "operator", "chief"} {
		t.Run(role, func(t *testing.T) {
			a := testApp(t)
			chief, _, _, card := fixtures(t, a)
			_, author, custodian, fake := telegramFixture(t, a, card)
			account := "1200"
			if role != "collector" {
				if role == "chief" {
					if err := a.db.QueryRow("SELECT id FROM custodians WHERE kind='chief'").Scan(&custodian); err != nil {
						t.Fatal(err)
					}
					account = "1210"
				} else {
					custodian = id()
					if _, err := a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,'Test operator','operator')", custodian); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := a.db.Exec("UPDATE users SET role=$1,custodian_id=$2 WHERE id=$3", role, custodian, author.ID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := a.tx()
			if err != nil {
				t.Fatal(err)
			}
			_, err = put(tx, "test_funding", id(), id(), chief.ID, time.Now(), time.Now(), []Posting{{Account: account, Side: "debit", Amount: 6000000, Custodian: custodian}, {Account: "3100", Side: "credit", Amount: 6000000}}, "")
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			cash := func(want int64) {
				t.Helper()
				var got int64
				if err := a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account=$1 AND custodian_id=$2", account, custodian).Scan(&got); err != nil || got != want {
					t.Fatal("cash", got, want, err)
				}
			}
			send := func(update M) {
				t.Helper()
				if status := telegramRequest(t, a, update); status != 200 {
					t.Fatal(status)
				}
			}
			send(telegramMessageUpdate(7101, 555, "зп 50000 За сентябрь"))
			var draftID string
			if err = a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:7101' AND kind='expense' AND payload->>'comment'='За сентябрь'").Scan(&draftID); err != nil {
				t.Fatal(err)
			}
			if !fake.contains("Источник: наличные автора") {
				t.Fatal("salary preview missing")
			}
			cash(6000000)
			send(telegramMessageUpdate(7101, 555, "зп 50000 За сентябрь"))
			send(telegramButtonUpdate(7102, 556, "confirm", draftID))
			cash(6000000)
			send(telegramButtonUpdate(7103, 555, "confirm", draftID))
			cash(1000000)
			send(telegramButtonUpdate(7104, 555, "confirm", draftID))
			cash(1000000)
			var debit, credit int64
			if err = a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' AND account='5100' AND category='salary' THEN amount_cents ELSE 0 END),0),COALESCE(SUM(CASE WHEN side='credit' THEN amount_cents ELSE 0 END),0) FROM postings WHERE entry_id IN (SELECT id FROM journal_entries WHERE event_id=$1)", draftID).Scan(&debit, &credit); err != nil || debit != 5000000 || credit != debit {
				t.Fatal(debit, credit, err)
			}
			reportCheck := func(expenses, profit string) {
				t.Helper()
				w := httptest.NewRecorder()
				a.report(w, httptest.NewRequest(http.MethodGet, "/api/report", nil), chief)
				var report M
				if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 {
					t.Fatal(w.Code, err)
				}
				summary, ok := report["summary"].(map[string]interface{})
				if !ok || summary["expenses"] != expenses || summary["profit"] != profit || summary["merchant_payable"] != "0.00" || summary["balance_difference"] != "0.00" {
					t.Fatal("salary report disagrees with ledger", report)
				}
			}
			reportCheck("50000.00", "-50000.00")
			send(telegramMessageUpdate(7105, 555, "зп 10001 Не хватает"))
			var insufficientID string
			if err = a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:7105'").Scan(&insufficientID); err != nil {
				t.Fatal(err)
			}
			send(telegramButtonUpdate(7106, 555, "confirm", insufficientID))
			cash(1000000)
			if !fake.contains("недостаточно денег") {
				t.Fatal("insufficient cash not rejected")
			}
			send(telegramButtonUpdate(7107, 555, "reject", insufficientID))
			cash(1000000)
			if code, out := req(t, a.reverseDraft, chief, M{"id": draftID, "reason": "Test salary correction"}); code != 200 {
				t.Fatal(code, out)
			}
			cash(6000000)
			reportCheck("0.00", "0.00")
			// A persisted expense must never debit another person's cash.
			send(telegramMessageUpdate(7108, 555, "ЗП 1 Проверка источника"))
			var alteredID string
			if err = a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:7108'").Scan(&alteredID); err != nil {
				t.Fatal(err)
			}
			if _, err = a.db.Exec("UPDATE drafts SET payload=jsonb_set(payload,'{expense_items,0,source_id}',to_jsonb($1::text)) WHERE id=$2", id(), alteredID); err != nil {
				t.Fatal(err)
			}
			send(telegramButtonUpdate(7109, 555, "confirm", alteredID))
			cash(6000000)
			if !fake.contains("Источник или категория") {
				t.Fatal("tampered source accepted")
			}
		})
	}
}
