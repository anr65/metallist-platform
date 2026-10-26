package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func promptDeletionSeen(fake *fakeTelegram, chat, message int64) bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, call := range fake.calls {
		if str(call, "method") == "deleteMessage" && int64(call["chat_id"].(float64)) == chat && int64(call["message_id"].(float64)) == message {
			return true
		}
	}
	return false
}
func TestTelegramPromptConfirmationFollowsInput(t *testing.T) {
	for _, operation := range []string{"withdraw", "expense", "handover", "transfer"} {
		t.Run(operation, func(t *testing.T) {
			a, _, sender, _, _, fake := handoverFixture(t)
			if telegramRequest(t, a, telegramMessageUpdate(91001, 555, "/"+operation)) != 200 {
				t.Fatal("menu")
			}
			prompt := a.telegramPromptMessage(sender, telegramTestGroup, operation)
			if prompt <= 0 {
				t.Fatal("missing prompt id")
			}
			input := "1234 10/0"
			switch operation {
			case "expense":
				input = "1234 прогрев 10"
			case "handover":
				input = "10"
			case "transfer":
				d, err := a.telegramTransferDialog(sender)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.telegramTransferSelectionForTest(sender, telegramTestGroup, prompt, 91002, "tr:"+d.Token+":0"); err != "" {
					t.Fatal(err)
				}
				input = "10"
			}
			fake.mu.Lock()
			beforeInput := len(fake.calls)
			fake.mu.Unlock()
			if code := telegramRequest(t, a, telegramMessageUpdate(91003, 555, input)); code != 200 {
				t.Fatal("input", code)
			}
			draft := uiDraft(t, a, 91003)
			confirmation := uiCard(t, a, draft, telegramTestGroup)
			if confirmation == prompt {
				t.Fatal("confirmation remains above user input")
			}
			fake.mu.Lock()
			calls := append([]M(nil), fake.calls[beforeInput:]...)
			fake.mu.Unlock()
			if len(calls) < 2 || str(calls[0], "method") != "sendMessage" || !strings.Contains(str(calls[0], "text"), "Подтвердите") {
				t.Fatal("no fresh confirmation after input", calls)
			}
			if !promptDeletionSeen(fake, telegramTestGroup, prompt) {
				t.Fatal("hint remains")
			}
			beforeConfirm := fake.countMethod("sendMessage")
			if text, done := a.telegramCallback(sender, telegramTestGroup, confirmation, "confirm:"+draft); !done {
				t.Fatal(text)
			}
			if uiCard(t, a, draft, telegramTestGroup) != confirmation || fake.countMethod("sendMessage") != beforeConfirm {
				t.Fatal("final did not replace confirmation")
			}
			if promptDeletionSeen(fake, telegramTestGroup, confirmation) {
				t.Fatal("deleted confirmation")
			}
		})
	}
}

// Use the production selection handler with the real bot hint ID.
func (a *App) telegramTransferSelectionForTest(u telegramActor, chat, msg, update int64, data string) string {
	text, _ := a.telegramTransferInputCallback(u, chat, msg, update, data)
	if text != "Получатель выбран" {
		return text
	}
	return ""
}

func TestTelegramPrivateRepaymentDeletesHintAfterFreshPreview(t *testing.T) {
	a, _, sender, _, _, fake := repaymentFixture(t)
	if telegramRequest(t, a, telegramMessageUpdateInChat(92001, 555, 555, "private", "/repay")) != 200 {
		t.Fatal("menu")
	}
	d := repaymentDialogForTest(t, a, sender.ID)
	prompt := d.PromptMessageID
	if prompt <= 0 {
		t.Fatal("hint missing")
	}
	update := privateTransferButton(92002, 555, "rm", d.Token+":0")
	update["callback_query"].(M)["message"].(M)["message_id"] = prompt
	if telegramRequest(t, a, update) != 200 {
		t.Fatal("merchant")
	}
	if telegramRequest(t, a, telegramMessageUpdateInChat(92003, 555, 555, "private", "10")) != 200 {
		t.Fatal("amount")
	}
	draft := uiDraft(t, a, 92003)
	confirmation := uiCard(t, a, draft, 555)
	if confirmation == prompt || !promptDeletionSeen(fake, 555, prompt) {
		t.Fatal("private prompt was reused or not deleted")
	}
	if promptDeletionSeen(fake, 555, confirmation) {
		t.Fatal("confirmation removed")
	}
}

func TestTelegramPromptDeletionFailureRetriesWithoutPosting(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, custodian, _ := telegramFixture(t, a, card)
	u := telegramActor{User: collector, CustodianID: custodian}
	telegramRequest(t, a, telegramMessageUpdate(93001, 555, "/withdraw"))
	prompt := a.telegramPromptMessage(u, telegramTestGroup, "withdraw")
	var fail atomic.Bool
	fail.Store(true)
	var deletes, edits, sends atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deleteMessage"):
			deletes.Add(1)
			if fail.Load() {
				w.WriteHeader(503)
				return
			}
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			edits.Add(1)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sends.Add(1)
		}
		json.NewEncoder(w).Encode(M{"ok": true, "result": M{"message_id": 3000}})
	}))
	defer api.Close()
	t.Setenv("TELEGRAM_API_BASE", api.URL)
	if telegramRequest(t, a, telegramMessageUpdate(93002, 555, "1234 10/0")) != 200 {
		t.Fatal("cleanup failure blocked confirmation")
	}
	draft := uiDraft(t, a, 93002)
	if uiCard(t, a, draft, telegramTestGroup) == prompt {
		t.Fatal("preview above input")
	}
	var pending, entries int
	a.db.QueryRow("SELECT count(*) FROM telegram_prompt_deletions WHERE last_error<>''").Scan(&pending)
	a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", draft).Scan(&entries)
	if pending != 1 || entries != 0 {
		t.Fatal("queue or money changed", pending, entries)
	}
	fail.Store(false)
	a.db.Exec("UPDATE telegram_prompt_deletions SET next_attempt_at=now()")
	restarted := &App{db: a.db, storage: a.storage, location: a.location}
	if err := restarted.telegramCleanupPrompts(10); err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow("SELECT count(*) FROM telegram_prompt_deletions").Scan(&pending)
	if pending != 0 || sends.Load() != 1 || edits.Load() != 0 || deletes.Load() != 2 {
		t.Fatal("cleanup sent another preview or edited hint", pending, sends.Load(), edits.Load(), deletes.Load())
	}
}

func TestTelegramPromptCleanupNeverDeletesLegacyCanonicalCard(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, _, _, fake := telegramFixture(t, a, card)
	telegramRequest(t, a, telegramMessageUpdate(94001, 555, "/withdraw 1234 10/0"))
	draft := uiDraft(t, a, 94001)
	canonical := uiCard(t, a, draft, telegramTestGroup)
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	if err = queueTelegramPromptDeletion(tx, telegramTestGroup, canonical); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = a.telegramCleanupPrompts(10); err != nil {
		t.Fatal(err)
	}
	if promptDeletionSeen(fake, telegramTestGroup, canonical) {
		t.Fatal("deleted legacy canonical card")
	}
	// A callback could bind an ID after enqueueing; the worker rechecks it.
	a.db.Exec("INSERT INTO telegram_prompt_deletions(chat_id,message_id) VALUES($1,$2)", telegramTestGroup, canonical)
	if err = a.telegramCleanupPrompts(10); err != nil {
		t.Fatal(err)
	}
	if promptDeletionSeen(fake, telegramTestGroup, canonical) {
		t.Fatal("worker deleted canonical card")
	}
}

func TestTelegramCardStatusDeletesPromptAfterSuccessfulInput(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, custodian, fake := telegramFixture(t, a, card)
	u := telegramActor{User: collector, CustodianID: custodian}
	telegramRequest(t, a, telegramMessageUpdate(95001, 555, "/card_status"))
	d, err := a.cardStatusDialog(u, telegramTestGroup)
	if err != nil {
		t.Fatal(err)
	}
	telegramRequest(t, a, telegramMessageUpdate(95002, 555, "1234"))
	next, err := a.cardStatusDialog(u, telegramTestGroup)
	if err != nil {
		t.Fatal(err)
	}
	if d.PromptMessageID == next.PromptMessageID || !promptDeletionSeen(fake, telegramTestGroup, d.PromptMessageID) {
		t.Fatal("partial input kept old hint")
	}
	telegramRequest(t, a, telegramMessageUpdate(95003, 555, "Блок"))
	if !fake.contains("Карта ••••1234: Блок.") || !promptDeletionSeen(fake, telegramTestGroup, next.PromptMessageID) {
		t.Fatal("final hint remains")
	}
}

func TestTelegramPromptRemainsUntilPreviewDelivered(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, custodian, fake := telegramFixture(t, a, card)
	u := telegramActor{User: collector, CustodianID: custodian}
	telegramRequest(t, a, telegramMessageUpdate(96001, 555, "/withdraw"))
	prompt := a.telegramPromptMessage(u, telegramTestGroup, "withdraw")
	telegramRequest(t, a, telegramMessageUpdate(96002, 555, "1234 abc/0"))
	if promptDeletionSeen(fake, telegramTestGroup, prompt) {
		t.Fatal("invalid input deleted hint")
	}
	var fail atomic.Bool
	fail.Store(true)
	var sends, deletes atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sends.Add(1)
			if fail.Load() {
				w.WriteHeader(503)
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deletes.Add(1)
		}
		json.NewEncoder(w).Encode(M{"ok": true, "result": M{"message_id": 4000}})
	}))
	defer api.Close()
	t.Setenv("TELEGRAM_API_BASE", api.URL)
	corrected := telegramMessageUpdate(96003, 555, "1234 10/0")
	if telegramRequest(t, a, corrected) != 500 {
		t.Fatal("failed preview delivery acknowledged")
	}
	if deletes.Load() != 0 {
		t.Fatal("deleted hint before preview delivery")
	}
	fail.Store(false)
	if telegramRequest(t, a, corrected) != 200 {
		t.Fatal("preview retry")
	}
	if sends.Load() != 2 || deletes.Load() != 2 {
		t.Fatal("preview or cleanup retry", sends.Load(), deletes.Load())
	} // hint and previous error
	draft := uiDraft(t, a, 96003)
	if uiCard(t, a, draft, telegramTestGroup) == prompt {
		t.Fatal("retry reused hint")
	}
	var count int
	a.db.QueryRow("SELECT count(*) FROM drafts WHERE id=$1", draft).Scan(&count)
	if count != 1 {
		t.Fatal("duplicated draft")
	}
}
