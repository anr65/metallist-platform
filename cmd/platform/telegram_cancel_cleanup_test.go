package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestTelegramCancelCashExpenseDeletesOwnHint(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "picker", true: "amount"}[selected], func(t *testing.T) {
			a, _, sender, other, fake := transferFixture(t)
			var prompt int64
			if selected {
				_, prompt = cashExpenseChoice(t, a, sender, 98001, "taxi")
			} else {
				telegramRequest(t, a, telegramMessageUpdate(98001, 555, "/cashexpense"))
				prompt = a.telegramPromptMessage(sender, telegramTestGroup, "cashexpense")
			}
			telegramRequest(t, a, telegramMessageUpdate(98002, 556, "/cashexpense"))
			otherPrompt := a.telegramPromptMessage(other, telegramTestGroup, "cashexpense")
			for i := int64(0); i < 2; i++ {
				if code := telegramRequest(t, a, telegramMessageUpdate(98003+i, 555, "/cancel")); code != 200 {
					t.Fatal(code)
				}
			}
			if a.telegramPromptMessage(sender, telegramTestGroup, "cashexpense") != 0 || a.telegramPromptMessage(other, telegramTestGroup, "cashexpense") != otherPrompt {
				t.Fatal("dialog scope")
			}
			if fake.countMethod("deleteMessage") != 1 {
				t.Fatal("cleanup not idempotent")
			}
			call := lastTelegramCall(t, fake, "deleteMessage")
			if call["message_id"] != float64(prompt) {
				t.Fatal("wrong message deleted", call)
			}
			telegramRequest(t, a, telegramMessageUpdate(98005, 555, "10"))
			var drafts int
			a.db.QueryRow("SELECT count(*) FROM drafts").Scan(&drafts)
			if drafts != 0 {
				t.Fatal("cancel created financial draft")
			}
		})
	}
}

func TestTelegramCancelCashExpenseDeletionRetriesAfterRestart(t *testing.T) {
	a, _, sender, _, _ := transferFixture(t)
	telegramRequest(t, a, telegramMessageUpdate(99001, 555, "/cashexpense"))
	prompt := a.telegramPromptMessage(sender, telegramTestGroup, "cashexpense")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(M{"ok": true, "result": M{"message_id": 3000}})
	}))
	defer api.Close()
	old := os.Getenv("TELEGRAM_API_BASE")
	t.Setenv("TELEGRAM_API_BASE", api.URL)
	if telegramRequest(t, a, telegramMessageUpdate(99002, 555, "/cancel")) != 200 {
		t.Fatal("cancel blocked by Telegram deletion failure")
	}
	var pending int
	a.db.QueryRow("SELECT count(*) FROM telegram_prompt_deletions WHERE message_id=$1 AND last_error<>''", prompt).Scan(&pending)
	if pending != 1 {
		t.Fatal("retry lost")
	}
	t.Setenv("TELEGRAM_API_BASE", old)
	a.db.Exec("UPDATE telegram_prompt_deletions SET next_attempt_at=now()")
	restarted := &App{db: a.db, storage: a.storage, location: a.location}
	if err := restarted.telegramCleanupPrompts(10); err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow("SELECT count(*) FROM telegram_prompt_deletions").Scan(&pending)
	if pending != 0 {
		t.Fatal("retry did not clean hint")
	}
}
