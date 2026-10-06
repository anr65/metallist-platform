package main

import (
	"strings"
	"testing"
	"time"
)

func transferDialogForTest(t *testing.T, a *App, u User, from string) telegramTransferDialog {
	t.Helper()
	d, err := a.telegramTransferDialog(telegramActor{User: u, CustodianID: from})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func assertNoTransferDraft(t *testing.T, a *App) {
	t.Helper()
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM drafts WHERE kind='transfer'").Scan(&count); err != nil || count != 0 {
		t.Fatal("unexpected transfer draft", count, err)
	}
}

func TestTelegramTransferInlineFlow(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, sender, from, fake := telegramFixture(t, a, card)
	to := id()
	if _, err := a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,'Получатель','collector')", to); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash,telegram_id,custodian_id) VALUES($1,'recipient','Получатель','collector','x',556,$2)", id(), to); err != nil {
		t.Fatal(err)
	}
	actor := telegramActor{User: sender, CustodianID: from}
	// No cash: show balance and accept typed input without a zero-value shortcut.
	telegramRequest(t, a, telegramMessageUpdate(4101, 555, "/transfer"))
	d := transferDialogForTest(t, a, sender, from)
	choose := "tr:" + d.Token + ":0"
	telegramRequest(t, a, telegramButtonUpdate(4102, 556, "tr", d.Token+":0"))
	if transferDialogForTest(t, a, sender, from).Recipient.ID != "" {
		t.Fatal("other user selected recipient")
	}
	telegramRequest(t, a, telegramButtonUpdate(4103, 555, "tr", d.Token+":0"))
	if !fake.contains("Текущий баланс наличных: 0 ₽") || fake.contains("Все наличные: 0 ₽") {
		t.Fatal("zero balance shortcut", fake.calls)
	}
	telegramRequest(t, a, telegramMessageUpdate(4104, 555, "0"))
	assertNoTransferDraft(t, a)
	telegramRequest(t, a, telegramMessageUpdate(4105, 555, "/cancel"))
	if result, _ := a.telegramTransferInputCallback(actor, telegramTestGroup, 4101, 4106, choose); !strings.Contains(result, "недоступна") {
		t.Fatal(result)
	}
	// Stale session, expired session and inactive recipient cannot be used.
	telegramRequest(t, a, telegramMessageUpdate(4107, 555, "/transfer"))
	if result, _ := a.telegramTransferInputCallback(actor, telegramTestGroup, 4101, 4108, choose); !strings.Contains(result, "недоступна") {
		t.Fatal(result)
	}
	d = transferDialogForTest(t, a, sender, from)
	if _, err := a.db.Exec("UPDATE telegram_dialogs SET expires_at=now()-interval '1 second' WHERE user_id=$1", sender.ID); err != nil {
		t.Fatal(err)
	}
	if result, _ := a.telegramTransferInputCallback(actor, telegramTestGroup, 4107, 4109, "tr:"+d.Token+":0"); !strings.Contains(result, "недоступна") {
		t.Fatal(result)
	}
	telegramRequest(t, a, telegramMessageUpdate(4110, 555, "/transfer"))
	d = transferDialogForTest(t, a, sender, from)
	if _, err := a.db.Exec("UPDATE custodians SET active=false WHERE id=$1", to); err != nil {
		t.Fatal(err)
	}
	if result, _ := a.telegramTransferInputCallback(actor, telegramTestGroup, 4110, 4111, "tr:"+d.Token+":0"); !strings.Contains(result, "недоступен") {
		t.Fatal(result)
	}
	if _, err := a.db.Exec("UPDATE custodians SET active=true WHERE id=$1", to); err != nil {
		t.Fatal(err)
	}
	// Fund via the existing ledger helper, then choose all cash with kopecks.
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = put(tx, "test_funding", id(), id(), sender.ID, time.Now(), time.Now(), []Posting{{Account: "1200", Side: "debit", Amount: 10001, Custodian: from}, {Account: "3100", Side: "credit", Amount: 10001}}, ""); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	telegramRequest(t, a, telegramButtonUpdate(4112, 555, "tr", d.Token+":0"))
	if !fake.contains("Текущий баланс наличных: 100,01") {
		t.Fatal("missing balance", fake.calls)
	}
	telegramRequest(t, a, telegramButtonUpdate(4113, 555, "ta", d.Token+":all"))
	telegramRequest(t, a, telegramButtonUpdate(4113, 555, "ta", d.Token+":all"))
	var draftID string
	var amount int64
	if err = a.db.QueryRow("SELECT id,(payload->>'amount_cents')::bigint FROM drafts WHERE idempotency_key='telegram:4113'").Scan(&draftID, &amount); err != nil || amount != 10001 {
		t.Fatal("all cash preview", amount, err)
	}
	var entries int
	if err = a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='transfer'").Scan(&entries); err != nil || entries != 0 {
		t.Fatal("button posted cash before confirmation", entries, err)
	}
	telegramRequest(t, a, telegramButtonUpdate(4114, 555, "confirm", draftID))
	telegramRequest(t, a, telegramButtonUpdate(4115, 555, "confirm", draftID))
	assertTransferState(t, a, draftID, "draft", 1, 0)
	telegramRequest(t, a, telegramMessageUpdateInChat(4190, 556, 556, "private", "/start"))
	telegramRequest(t, a, privateTransferButton(4191, 556, "receive", draftID))
	if err = a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='transfer'").Scan(&entries); err != nil || entries != 1 {
		t.Fatal("duplicate all-cash transfer", entries, err)
	}
	// A recipient with history precedes an alphabetically earlier new recipient.
	another := id()
	if _, err = a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,'А Новый','collector')", another); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("INSERT INTO users(id,login,name,role,password_hash,custodian_id,telegram_id) VALUES($1,'another','А Новый','collector','x',$2,557)", id(), another); err != nil {
		t.Fatal(err)
	}
	recipients, err := a.telegramTransferRecipients(actor)
	if err != nil || len(recipients) != 2 || recipients[0].ID != to {
		t.Fatal("recent recipient ordering", recipients, err)
	}
	reverseRecipients, err := a.telegramTransferRecipients(telegramActor{User: User{Role: "collector"}, CustodianID: to})
	if err != nil || len(reverseRecipients) != 2 || reverseRecipients[0].ID != another {
		t.Fatal("incoming transfer affected recency", reverseRecipients, err)
	}
	// A draft cannot move a recipient above the most recent confirmed transfer.
	telegramRequest(t, a, telegramMessageUpdate(4116, 555, "/transfer 2 1"))
	recipients, err = a.telegramTransferRecipients(actor)
	if err != nil || recipients[0].ID != to {
		t.Fatal("draft affected recency", recipients, err)
	}
	// After funding and confirmation, the newly used recipient moves first.
	tx, err = a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = put(tx, "test_funding", id(), id(), sender.ID, time.Now(), time.Now(), []Posting{{Account: "1200", Side: "debit", Amount: 100, Custodian: from}, {Account: "3100", Side: "credit", Amount: 100}}, ""); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow("SELECT id FROM drafts WHERE idempotency_key='telegram:4116'").Scan(&draftID); err != nil {
		t.Fatal(err)
	}
	telegramRequest(t, a, telegramButtonUpdate(4117, 555, "confirm", draftID))
	telegramRequest(t, a, telegramMessageUpdateInChat(4192, 557, 557, "private", "/start"))
	telegramRequest(t, a, privateTransferButton(4193, 557, "receive", draftID))
	recipients, err = a.telegramTransferRecipients(actor)
	if err != nil || recipients[0].ID != another {
		t.Fatal("latest transfer did not move recipient first", recipients, err)
	}
}

func TestTelegramInlineTransferUsesAvailableCash(t *testing.T) {
	a, _, sender, _, fake := transferFixture(t)
	pending := newTransfer(t, a, 4201, "70")
	confirmTransferSender(t, a, sender, pending)
	telegramRequest(t, a, telegramMessageUpdate(4202, 555, "/transfer"))
	d := transferDialogForTest(t, a, sender.User, sender.CustodianID)
	telegramRequest(t, a, telegramButtonUpdate(4203, 555, "tr", d.Token+":0"))
	d = transferDialogForTest(t, a, sender.User, sender.CustodianID)
	if d.Balance != 10000 || d.Available != 3000 || !fake.contains("Зарезервировано: 70 ₽") || !fake.contains("Доступно для перевода: 30 ₽") {
		t.Fatal("reserved cash offered", d, fake.calls)
	}
	telegramRequest(t, a, telegramButtonUpdate(4204, 555, "ta", d.Token+":all"))
	var draft string
	var amount, senderTelegram int64
	if err := a.db.QueryRow("SELECT id,(payload->>'amount_cents')::bigint,(payload->>'telegram_sender_id')::bigint FROM drafts WHERE idempotency_key='telegram:4204'").Scan(&draft, &amount, &senderTelegram); err != nil || amount != 3000 || senderTelegram != 555 {
		t.Fatal("all available cash", amount, senderTelegram, err)
	}
	confirmTransferSender(t, a, sender, draft)
	assertTransferState(t, a, draft, "draft", 1, 0)
}
