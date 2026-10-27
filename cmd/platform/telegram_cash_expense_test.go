package main

import (
	"fmt"
	"strings"
	"testing"
)

func cashExpenseChoice(t *testing.T, a *App, actor telegramActor, update int64, category string) (string, int64) {
	t.Helper()
	if telegramRequest(t, a, telegramMessageUpdate(update, 555, "/cashexpense@synthetic_bot")) != 200 {
		t.Fatal("start")
	}
	var token string
	var prompt int64
	if err := a.db.QueryRow("SELECT context->>'token',(context->>'prompt_message_id')::bigint FROM telegram_dialogs WHERE user_id=$1", actor.ID).Scan(&token, &prompt); err != nil {
		t.Fatal(err)
	}
	message, _ := a.telegramCashExpenseSelection(actor, telegramTestGroup, prompt, "ce:"+token+":"+category)
	if message != "Введите сумму расхода" {
		t.Fatal(message)
	}
	return token, prompt
}

func TestTelegramCashExpenseCategoriesPostingAndReversal(t *testing.T) {
	for _, category := range telegramCashExpenseCategories {
		t.Run(category, func(t *testing.T) {
			a, chief, sender, _, fake := transferFixture(t)
			_, prompt := cashExpenseChoice(t, a, sender, 95001, category)
			if telegramRequest(t, a, telegramMessageUpdate(95002, 555, "10,01 поездка")) != 200 {
				t.Fatal("input")
			}
			draft := uiDraft(t, a, 95002)
			preview := uiCard(t, a, draft, telegramTestGroup)
			if preview == prompt || !fake.contains("Источник: наличные автора") || !fake.contains(telegramCategoryName(category)) {
				t.Fatal("preview")
			}
			for i := int64(0); i < 2; i++ {
				telegramRequest(t, a, telegramButtonUpdate(95003+i, 555, "confirm", draft))
			}
			var cash int64
			var entries int
			a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account='1200' AND custodian_id=$1", sender.CustodianID).Scan(&cash)
			a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draft).Scan(&entries)
			if cash != 8999 || entries != 1 || !fake.contains("Баланс: 89,99 ₽") {
				t.Fatal(cash, entries)
			}
			var debit, credit int64
			var account string
			if err := a.db.QueryRow("SELECT account,amount_cents FROM postings WHERE entry_id IN (SELECT id FROM journal_entries WHERE event_id=$1) AND side='debit' AND category=$2", draft, category).Scan(&account, &debit); err != nil {
				t.Fatal(err)
			}
			a.db.QueryRow("SELECT amount_cents FROM postings WHERE entry_id IN (SELECT id FROM journal_entries WHERE event_id=$1) AND side='credit' AND custodian_id=$2", draft, sender.CustodianID).Scan(&credit)
			want := "5100"
			if category == "other" {
				want = "5900"
			}
			if account != want || debit != 1001 || credit != debit {
				t.Fatal(account, debit, credit)
			}
			if code, out := req(t, a.reverseDraft, chief, M{"id": draft, "reason": "Тестовое исправление"}); code != 200 {
				t.Fatal(code, out)
			}
			var sum int64
			a.db.QueryRow("SELECT SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) FROM postings WHERE account='1200' AND custodian_id=$1", sender.CustodianID).Scan(&sum)
			if sum != 10000 {
				t.Fatal("reversal", sum)
			}
		})
	}
}

func TestTelegramCashExpenseValidationReservesAndErrorCleanup(t *testing.T) {
	a, _, sender, other, fake := transferFixture(t)
	token, prompt := cashExpenseChoice(t, a, sender, 96001, "taxi")
	if message, _ := a.telegramCashExpenseSelection(other, telegramTestGroup, prompt, "ce:"+token+":material"); message == "Введите сумму расхода" {
		t.Fatal("other author selected")
	}
	for i, input := range []string{"0", "-1", "abc", "1,001"} {
		telegramRequest(t, a, telegramMessageUpdate(96002+int64(i), 555, input))
	}
	var count int
	a.db.QueryRow("SELECT count(*) FROM telegram_input_errors WHERE operation='cashexpense'").Scan(&count)
	if count != 4 {
		t.Fatal("typed errors", count)
	}
	if telegramRequest(t, a, telegramMessageUpdate(96006, 555, "30")) != 200 {
		t.Fatal("amount")
	}
	draft := uiDraft(t, a, 96006)
	a.db.QueryRow("SELECT count(*) FROM telegram_input_errors WHERE operation='cashexpense'").Scan(&count)
	if count != 0 {
		t.Fatal("errors not cleaned")
	}
	transfer := newTransfer(t, a, 96007, "80")
	confirmTransferSender(t, a, sender, transfer)
	telegramRequest(t, a, telegramButtonUpdate(96008, 555, "confirm", draft))
	var status string
	a.db.QueryRow("SELECT status FROM drafts WHERE id=$1", draft).Scan(&status)
	if status != "draft" {
		t.Fatal("spent reserved cash", status)
	}
	if !fake.contains("Расход из наличных: Такси") {
		t.Fatal("category hint")
	}
	telegramRequest(t, a, telegramMessageUpdate(96009, 555, "/cashexpense"))
	if message, _ := a.telegramCashExpenseSelection(sender, telegramTestGroup, prompt, "ce:"+token+":taxi"); message == "Введите сумму расхода" {
		t.Fatal("stale button accepted")
	}
	telegramRequest(t, a, telegramMessageUpdate(96010, 555, "/cancel"))
	telegramRequest(t, a, telegramMessageUpdate(96011, 555, "10"))
	a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key='telegram:96011'").Scan(&count)
	if count != 0 {
		t.Fatal("cancelled input consumed")
	}
}

func TestTelegramCashExpenseWebhookChoiceAndPreviewRetry(t *testing.T) {
	a, _, sender, _, fake := transferFixture(t)
	telegramRequest(t, a, telegramMessageUpdate(97001, 555, "/cashexpense"))
	var token string
	var prompt int64
	a.db.QueryRow("SELECT context->>'token',(context->>'prompt_message_id')::bigint FROM telegram_dialogs WHERE user_id=$1", sender.ID).Scan(&token, &prompt)
	update := telegramButtonUpdate(97002, 555, "ce", token+":material")
	update["callback_query"].(M)["message"].(M)["message_id"] = prompt
	if code := telegramRequest(t, a, update); code != 200 {
		t.Fatal(code)
	}
	telegramRequest(t, a, telegramMessageUpdate(97003, 555, "1к"))
	draft := uiDraft(t, a, 97003)
	telegramRequest(t, a, telegramMessageUpdate(97003, 555, "1к"))
	var count int
	a.db.QueryRow("SELECT count(*) FROM drafts WHERE idempotency_key=$1", fmt.Sprint("telegram:97003")).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate draft", count)
	}
	if !fake.contains("Подтвердите расход «Материал»") {
		t.Fatal("wrong heading")
	}
	telegramRequest(t, a, telegramButtonUpdate(97004, 555, "reject", draft))
	var status string
	a.db.QueryRow("SELECT status FROM drafts WHERE id=$1", draft).Scan(&status)
	if status != "rejected" || strings.Contains(str(lastTelegramCall(t, fake, "editMessageText"), "text"), "Баланс:") {
		t.Fatal(status)
	}
}
