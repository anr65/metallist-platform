package main

import (
	"strings"
	"testing"
)

func TestTelegramSixDigitCardSelection(t *testing.T) {
	a := testApp(t)
	_, _, bank, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	second := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Test','400000******1234','1234')", second, bank); err != nil {
		t.Fatal(err)
	}
	// Use synthetic Luhn-valid PANs; one in the vault, one in the correction column.
	makePAN := func(base string) string {
		for n := 0; n < 10; n++ {
			pan := "400000" + string(rune('0'+n)) + base
			if validLuhn(pan) {
				return pan
			}
		}
		t.Fatal("invalid test PAN")
		return ""
	}
	firstPAN, secondPAN := "0000000000061234", makePAN("000221234")
	cipher, err := sealSensitive([]byte(secondPAN), []byte(second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("UPDATE cards SET pan_ciphertext=$2 WHERE id=$1", second, cipher); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ suffix, want string }{{"061234", card}, {"221234", second}} {
		got, mask, err := a.telegramCard(telegramActor{}, tc.suffix)
		if err != nil || got != tc.want || mask != "****1234" {
			t.Fatal("wrong suffix selection", got, mask, err)
		}
	}
	if _, _, err = a.telegramCard(telegramActor{}, "1234"); err == nil || !strings.Contains(err.Error(), "Введите последние 6 цифр") {
		t.Fatal("missing collision guidance", err)
	}
	for _, suffix := range []string{"123", "12345", "1234567", "abcdef", "001234"} {
		if _, _, err = a.telegramCard(telegramActor{}, suffix); err == nil {
			t.Fatal("accepted invalid/unknown suffix", suffix)
		}
	}
	// A later ambiguous row rejects the whole mixed-length package.
	telegramRequest(t, a, telegramMessageUpdate(9300, 555, "/withdraw 061234 1к/20\n1234 2к/30"))
	var count int
	if err = a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key='telegram:9300'").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial package accepted", count, err)
	}
	telegramRequest(t, a, telegramMessageUpdate(9301, 555, "/withdraw 061234 1к/20\n221234 2к/30"))
	if err = a.db.QueryRow("SELECT jsonb_array_length(payload->'withdrawal_items') FROM drafts WHERE idempotency_key='telegram:9301'").Scan(&count); err != nil || count != 2 {
		t.Fatal("six digit withdrawal failed", count, err)
	}
	var draftID string
	if err = a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:9301'").Scan(&draftID); err != nil {
		t.Fatal(err)
	}
	telegramRequest(t, a, telegramButtonUpdate(9303, 555, "confirm", draftID))
	telegramRequest(t, a, telegramButtonUpdate(9304, 555, "confirm", draftID))
	if err = a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='withdrawal' AND event_id=$1", draftID).Scan(&count); err != nil || count != 1 {
		t.Fatal("six digit posting duplicated or missing", count, err)
	}
	var firstDebit, secondDebit int64
	if err = a.db.QueryRow("SELECT COALESCE(sum(amount_cents) FILTER (WHERE card_id=$2),0),COALESCE(sum(amount_cents) FILTER (WHERE card_id=$3),0) FROM postings WHERE entry_id=(SELECT id FROM journal_entries WHERE event_type='withdrawal' AND event_id=$1) AND account='1100' AND side='credit'", draftID, card, second).Scan(&firstDebit, &secondDebit); err != nil || firstDebit != 100000 || secondDebit != 200000 {
		t.Fatal("wrong cards debited", firstDebit, secondDebit, err)
	}
	telegramRequest(t, a, telegramMessageUpdate(9302, 555, "/expense 221234 комса 10"))
	var selected string
	if err = a.db.QueryRow("SELECT payload->'expense_items'->0->>'card_id' FROM drafts WHERE idempotency_key='telegram:9302'").Scan(&selected); err != nil || selected != second {
		t.Fatal("six digit expense failed", selected, err)
	}
	if fake.contains(firstPAN) || fake.contains(secondPAN) {
		t.Fatal("PAN disclosed")
	}
	// Six digits must still be unique, including after a PAN correction.
	cipher, err = sealSensitive([]byte(firstPAN), []byte(second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("UPDATE cards SET pan_ciphertext=$2 WHERE id=$1", second, cipher); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.telegramCard(telegramActor{}, "061234"); err == nil || !strings.Contains(err.Error(), "совпадают") {
		t.Fatal("six digit collision accepted", err)
	}
	// Unreadable candidates cannot be silently skipped.
	if _, err = a.db.Exec("UPDATE cards SET pan_ciphertext=$2 WHERE id=$1", second, []byte("invalid")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.telegramCard(telegramActor{}, "061234"); err == nil {
		t.Fatal("unreadable candidate skipped")
	}
	if _, err = a.db.Exec("UPDATE cards SET status='retired' WHERE id=$1", second); err != nil {
		t.Fatal(err)
	}
	if got, _, err := a.telegramCard(telegramActor{}, "1234"); err != nil || got != card {
		t.Fatal("inactive card blocks selection", err)
	}
	if got, _, err := a.telegramCard(telegramActor{}, "061234"); err != nil || got != card {
		t.Fatal("inactive unreadable card blocks selection", err)
	}
}

func TestTelegramMixedCardSuffixDialog(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	telegramFixture(t, a, card)
	telegramRequest(t, a, telegramMessageUpdate(9400, 555, "/withdraw"))
	telegramRequest(t, a, telegramMessageUpdate(9401, 555, "1234 1к/0\n061234 2к/0"))
	var count int
	if err := a.db.QueryRow("SELECT jsonb_array_length(payload->'withdrawal_items') FROM drafts WHERE idempotency_key='telegram:9401'").Scan(&count); err != nil || count != 2 {
		t.Fatal("mixed suffix dialog failed", count, err)
	}
}
