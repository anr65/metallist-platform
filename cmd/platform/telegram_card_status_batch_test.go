package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTelegramCardStatusMultiline(t *testing.T) {
	for _, tc := range []struct {
		command, text string
		count         int
		status        string
	}{
		{"block_card", "6663\n0802\n8666\n7772\n7491\n4496\n4469\n1975\n5706", 9, "blocked"},
		{"block_card", "1234\r\n\r\n0802\n1234", 2, "blocked"},
		{"card_status", "1234\n0802 Лимит", 2, "limit"},
		{"card_status", "1234 Лимит\n0802 limit", 2, "limit"},
		{"card_status", "1234\n0802\nЛимит", 2, "limit"},
	} {
		items, err := telegramCardStatusItems(strings.Fields(tc.text), tc.command)
		if err != nil || len(items) != tc.count {
			t.Fatalf("%q: %v %v", tc.text, items, err)
		}
		for _, item := range items {
			if item.Status != tc.status {
				t.Fatal(item)
			}
		}
	}
	for _, tc := range []struct{ command, text string }{
		{"block_card", ""}, {"block_card", "1234\nwrong"}, {"block_card", "1234 Лимит"},
		{"card_status", "1234 unknown"},
		{"card_status", "1234 Лимит\n1234 Блок"},
		{"block_card", "4111111111111111"},
	} {
		if _, err := telegramCardStatusItems(strings.Fields(tc.text), tc.command); err == nil {
			t.Fatalf("accepted %q", tc.text)
		}
	}
	selectors := []string{}
	for _, v := range []string{"0000", "0001", "0002", "0003", "0004", "0005", "0006", "0007", "0008", "0009", "0010", "0011", "0012", "0013", "0014", "0015", "0016", "0017", "0018", "0019", "0020"} {
		selectors = append(selectors, v)
	}
	if _, err := telegramCardStatusItems(selectors, "block_card"); err == nil {
		t.Fatal("accepted more than 20 cards")
	}
}

func TestTelegramCardStatusBatchDialog(t *testing.T) {
	a := testApp(t)
	_, _, bank, card := fixtures(t, a)
	_, collector, custodian, fake := telegramFixture(t, a, card)
	actor := telegramActor{User: collector, CustodianID: custodian}
	second := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Вторая','000000******0802','0802')", second, bank); err != nil {
		t.Fatal(err)
	}
	state := func(want string) {
		t.Helper()
		var count int
		if err := a.db.QueryRow("SELECT count(*) FROM cards WHERE id IN ($1,$2) AND status=$3", card, second, want).Scan(&count); err != nil || count != 2 {
			t.Fatal(want, count, err)
		}
	}
	if err := a.telegramCardStatus(actor, []string{"/block_card", "1234", "9999"}, "block_card", 10001); err == nil {
		t.Fatal("missing card accepted")
	}
	state("active")
	duplicate := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Дубль','111111******0802','0802')", duplicate, bank); err != nil {
		t.Fatal(err)
	}
	if err := a.telegramCardStatus(actor, []string{"/block_card", "1234", "0802"}, "block_card", 10002); err == nil {
		t.Fatal("ambiguous card accepted")
	}
	state("active")
	if _, err := a.db.Exec("DELETE FROM cards WHERE id=$1", duplicate); err != nil {
		t.Fatal(err)
	}
	var audits int
	a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='card_status_change'").Scan(&audits)
	if audits != 0 {
		t.Fatal("failed batch left audit changes", audits)
	}
	for i, text := range []string{"/block_card", "1234\n9999", "1234\n0802", "/card_status", "1234\n0802", "Лимит"} {
		update := telegramMessageUpdate(int64(10100+i), 555, text)
		if code := telegramRequest(t, a, update); code != 200 {
			t.Fatal(code)
		}
		if code := telegramRequest(t, a, update); code != 200 {
			t.Fatal(code)
		}
	}
	state("limit")
	var changes, commands, dialogs, postings int
	a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='card_status_change'").Scan(&changes)
	a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='card_status_command'").Scan(&commands)
	a.db.QueryRow("SELECT count(*) FROM telegram_dialogs WHERE user_id=$1", collector.ID).Scan(&dialogs)
	a.db.QueryRow("SELECT count(*) FROM postings").Scan(&postings)
	if changes != 4 || commands != 4 || dialogs != 0 || postings != 0 {
		t.Fatal(changes, commands, dialogs, postings)
	}

	// Cancellation and expiry must stop consuming ordinary messages.
	for i, text := range []string{"/block_card", "/cancel", "1234\n0802"} {
		if code := telegramRequest(t, a, telegramMessageUpdate(int64(10200+i), 555, text)); code != 200 {
			t.Fatal(code)
		}
	}
	state("limit")
	if code := telegramRequest(t, a, telegramMessageUpdate(10300, 555, "/block_card")); code != 200 {
		t.Fatal(code)
	}
	if _, err := a.db.Exec("UPDATE telegram_dialogs SET expires_at=now()-interval '1 second' WHERE user_id=$1", collector.ID); err != nil {
		t.Fatal(err)
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(10301, 555, "1234\n0802")); code != 200 {
		t.Fatal(code)
	}
	state("limit")
	if err := a.telegramCardStatus(actor, []string{"/card_status", "1234", "Лимит", card, "Блок"}, "card_status", 10302); err == nil {
		t.Fatal("conflicting UUID and suffix accepted")
	}
	state("limit")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	found := false
	for _, call := range fake.calls {
		if text, ok := call["text"].(string); ok && text == "Карта ••••1234: Лимит.\nКарта ••••0802: Лимит." {
			found = true
		}
	}
	if !found {
		t.Fatal("no success reply")
	}
}

func TestTelegramCardStatusNineCardsAndDeliveryRetry(t *testing.T) {
	a := testApp(t)
	_, _, bank, card := fixtures(t, a)
	_, collector, _, _ := telegramFixture(t, a, card)
	suffixes := []string{"6663", "0802", "8666", "7772", "7491", "4496", "4469", "1975", "5706"}
	for _, suffix := range suffixes {
		if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Пакет',$3,$4)", id(), bank, "000000******"+suffix, suffix); err != nil {
			t.Fatal(err)
		}
	}
	if code := telegramRequest(t, a, telegramMessageUpdate(10400, 555, "/block_card")); code != 200 {
		t.Fatal(code)
	}
	var fail atomic.Bool
	fail.Store(true)
	replies := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Swap(false) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var body M
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			replies = append(replies, str(body, "text"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":12345}}`))
	}))
	defer server.Close()
	t.Setenv("TELEGRAM_API_BASE", server.URL)
	update := telegramMessageUpdate(10401, 555, strings.Join(suffixes, "\n"))
	if code := telegramRequest(t, a, update); code != 500 {
		t.Fatal("expected result delivery failure", code)
	}
	if code := telegramRequest(t, a, update); code != 200 {
		t.Fatal("retry failed", code)
	}
	var changes, postings int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='card_status_command'", collector.ID).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM postings").Scan(&postings); err != nil {
		t.Fatal(err)
	}
	if changes != 9 || postings != 0 {
		t.Fatal("duplicate or monetary effects", changes, postings)
	}
	if len(replies) != 1 || len(strings.Split(replies[0], "\n")) != 9 {
		t.Fatal("incomplete retry result", replies)
	}
	for _, suffix := range suffixes {
		if !strings.Contains(replies[0], "Карта ••••"+suffix+": Блок.") {
			t.Fatal("missing card in result", suffix)
		}
	}
}
