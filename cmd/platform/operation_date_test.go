package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOperationOccurredAt(t *testing.T) {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	fallback := time.Date(2026, 10, 6, 17, 30, 0, 0, location)
	got, err := operationOccurredAt("2026-10-05", location, fallback)
	if err != nil || got.Format(time.RFC3339) != "2026-10-05T00:00:00+03:00" {
		t.Fatalf("wrong Moscow operation date: %v, %v", got, err)
	}
	got, err = operationOccurredAt("", location, fallback)
	if err != nil || !got.Equal(fallback) {
		t.Fatalf("historical fallback changed: %v, %v", got, err)
	}
	for _, invalid := range []string{"2026-02-29", "2026-10-32", "06.10.2026", "2026-10-06T12:00:00Z"} {
		if _, err = operationOccurredAt(invalid, location, fallback); err == nil {
			t.Errorf("accepted invalid date %q", invalid)
		}
	}
}

func TestWebOperationDatePersistedAndPosted(t *testing.T) {
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	var collector string
	if err := a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&collector); err != nil {
		t.Fatal(err)
	}
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = put(tx, "test_funding", id(), "operation-date-funding", chief.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 10000, Card: card}, {Account: "3100", Side: "credit", Amount: 10000}}, ""); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	body := M{"kind": "withdrawal", "card_id": card, "custodian_id": collector, "amount": "50.01", "date": "2026-10-05", "telegram_sender_confirmed": true}
	code, created := req(t, a.draft, chief, body)
	if code != 201 {
		t.Fatal(code, created)
	}
	draftID := created["id"].(string)
	var date string
	var telegram bool
	if err = a.db.QueryRow("SELECT payload->>'date',payload ? 'telegram_sender_confirmed' FROM drafts WHERE id=$1", draftID).Scan(&date, &telegram); err != nil || date != "2026-10-05" || telegram {
		t.Fatal(date, telegram, err)
	}
	if code, out := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "50.01"}); code != 200 {
		t.Fatal(code, out)
	}
	var occurredAt time.Time
	if err = a.db.QueryRow("SELECT occurred_at FROM journal_entries WHERE event_id=$1", draftID).Scan(&occurredAt); err != nil || occurredAt.In(a.location).Format(time.RFC3339) != "2026-10-05T00:00:00+03:00" {
		t.Fatal(occurredAt, err)
	}
	if code, out := req(t, a.confirmDraft, chief, M{"id": draftID, "version": "1", "confirm_amount": "50.01"}); code != 200 {
		t.Fatal(code, out)
	}
	var entries int
	if err = a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draftID).Scan(&entries); err != nil || entries != 1 {
		t.Fatal(entries, err)
	}
	for _, query := range []struct {
		day   string
		count int
	}{{"2026-10-05", 1}, {"2026-10-06", 0}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/drafts?date_from="+query.day+"&date_to="+query.day, nil)
		a.drafts(w, r, chief)
		var rows []M
		if err = json.Unmarshal(w.Body.Bytes(), &rows); err != nil || w.Code != 200 || len(rows) != query.count {
			t.Fatal("date filter", w.Code, w.Body.String(), err)
		}
	}
	body["date"] = "2026-02-30"
	if code, _ := req(t, a.draft, chief, body); code != 400 {
		t.Fatal("invalid draft date accepted", code)
	}
	if code, out := req(t, a.observation, chief, M{"card_id": card, "amount": "49.99", "date": "2026-10-04"}); code != 201 {
		t.Fatal(code, out)
	}
	if err = a.db.QueryRow("SELECT observed_at FROM observations WHERE card_id=$1", card).Scan(&occurredAt); err != nil || occurredAt.In(a.location).Format(time.RFC3339) != "2026-10-04T00:00:00+03:00" {
		t.Fatal(occurredAt, err)
	}
	if code, _ := req(t, a.observation, chief, M{"card_id": card, "amount": "49.99", "date": "2026-02-30"}); code != 400 {
		t.Fatal("invalid observation date accepted", code)
	}
}
