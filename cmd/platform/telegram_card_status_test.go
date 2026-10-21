package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func cardState(t *testing.T, a *App, card string) string {
	t.Helper()
	var status string
	if err := a.db.QueryRow("SELECT status FROM cards WHERE id=$1", card).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestCardStatusMenuThenSeparateReply(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, _, fake := telegramFixture(t, a, card)
	send := func(update int64, text string) {
		t.Helper()
		if code := telegramRequest(t, a, telegramMessageUpdate(update, 555, text)); code != 200 {
			t.Fatal(code, text)
		}
	}
	send(91001, "/card_status@metallistcash_bot")
	if cardState(t, a, card) != "active" {
		t.Fatal("menu changed a card")
	}
	var command string
	if err := a.db.QueryRow("SELECT command FROM telegram_dialogs WHERE user_id=$1", collector.ID).Scan(&command); err != nil || command != "card_status" {
		t.Fatal("menu did not begin input", command, err)
	}
	send(91002, "1234 блок @colleague @another")
	if cardState(t, a, card) != "blocked" {
		t.Fatal("screenshot reply was ignored")
	}
	fake.mu.Lock()
	lastReply := ""
	for _, call := range fake.calls {
		if str(call, "method") == "sendMessage" {
			lastReply = str(call, "text")
		}
	}
	fake.mu.Unlock()
	if lastReply != "Карта ••••1234: Блок." {
		t.Fatal("missing result", lastReply)
	}
	send(91002, "1234 блок @colleague @another")
	var audits, dialogs, postings int
	a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='card_status_command' AND object_id=$1", card).Scan(&audits)
	a.db.QueryRow("SELECT count(*) FROM telegram_dialogs WHERE user_id=$1", collector.ID).Scan(&dialogs)
	a.db.QueryRow("SELECT count(*) FROM postings").Scan(&postings)
	if audits != 1 || dialogs != 0 || postings != 0 {
		t.Fatal("duplicate effect, unfinished input or money movement", audits, dialogs, postings)
	}
	// A normal message without active input never changes a card.
	send(91003, "1234 Активна")
	if cardState(t, a, card) != "blocked" {
		t.Fatal("ordinary chat changed card")
	}
	send(91004, "/card_status 1234")
	send(91005, "Заблокирована")
	if cardState(t, a, card) != "blocked" {
		t.Fatal("status alias not accepted")
	}
	send(91006, "/card_status")
	send(91007, "1234")
	send(91008, "Лимит")
	if cardState(t, a, card) != "limit" {
		t.Fatal("three-step input failed")
	}
}

func TestBlockCardMenuExpiryCancelAndIsolation(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, _, fake := telegramFixture(t, a, card)
	send := func(update, sender int64, text string) {
		t.Helper()
		if code := telegramRequest(t, a, telegramMessageUpdate(update, sender, text)); code != 200 {
			t.Fatal(code)
		}
	}
	send(92001, 555, "/block_card@metallistcash_bot")
	send(92002, 999, "1234")
	if cardState(t, a, card) != "active" {
		t.Fatal("unlinked sender completed input")
	}
	// A registered second user has no access to the first user's pending input.
	var custodian string
	if err := a.db.QueryRow("SELECT custodian_id::text FROM users WHERE id=$1", collector.ID).Scan(&custodian); err != nil {
		t.Fatal(err)
	}
	otherCustodian := id()
	if _, err := a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,'Other','collector')", otherCustodian); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash,telegram_id,custodian_id) VALUES($1,'other-card-user','Other','collector','x',777,$2)", id(), otherCustodian); err != nil {
		t.Fatal(err)
	}
	send(92003, 777, "1234")
	if cardState(t, a, card) != "active" {
		t.Fatal("another actor consumed dialog")
	}
	send(92004, 555, "неверная карта")
	send(92005, 555, "1234")
	if cardState(t, a, card) != "blocked" {
		t.Fatal("separate suffix did not block")
	}
	send(92006, 555, "/card_status 1234 Активна")
	send(92007, 555, "/block_card")
	send(92008, 555, "/cancel")
	send(92009, 555, "1234")
	if cardState(t, a, card) != "active" {
		t.Fatal("cancelled input applied")
	}
	send(92010, 555, "/block_card")
	if _, err := a.db.Exec("UPDATE telegram_dialogs SET expires_at=now()-interval '1 minute' WHERE user_id=$1", collector.ID); err != nil {
		t.Fatal(err)
	}
	send(92011, 555, "1234")
	if cardState(t, a, card) != "active" {
		t.Fatal("expired input applied")
	}
	send(92012, 555, "/block_card")
	send(92013, 555, "/withdraw")
	// A stale status input may not consume an unrelated newer operation.
	if _, err := a.db.Exec("UPDATE telegram_dialogs SET context=jsonb_build_object('token','old','chat_id',$2::bigint,'last_update_id',1) WHERE user_id=$1", collector.ID, telegramTestGroup); err != nil {
		t.Fatal(err)
	}
	if err := a.telegramCardStatus(telegramActor{User: collector}, []string{"/block_card", "1234"}, "block_card", 92014, telegramCardStatusDialog{Token: "old", Command: "block_card", ChatID: telegramTestGroup}); err == nil {
		t.Fatal("stale input applied")
	}
	if cardState(t, a, card) != "active" {
		t.Fatal("stale dialog changed card")
	}
	if len(fake.calls) == 0 {
		t.Fatal("no Telegram replies")
	}
}

func TestCardStatusInputRejectsWrongChatAndOldUpdate(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, custodian, _ := telegramFixture(t, a, card)
	actor := telegramActor{User: collector, CustodianID: custodian}
	telegramRequest(t, a, telegramMessageUpdate(93002, 555, "/block_card"))
	if code := telegramRequest(t, a, telegramMessageUpdateInChat(93003, 555, -100123456, "supergroup", "1234")); code != 200 {
		t.Fatal(code)
	}
	if cardState(t, a, card) != "active" {
		t.Fatal("wrong group changed card")
	}
	message := &telegramMessage{Text: "1234"}
	message.Chat.ID = telegramTestGroup
	if err := a.telegramCardStatusInput(actor, message, 93001, ""); err == nil {
		t.Fatal("out-of-order input accepted")
	}
	if cardState(t, a, card) != "active" {
		t.Fatal("old update changed card")
	}
	telegramRequest(t, a, telegramMessageUpdate(93004, 555, "1234"))
	if cardState(t, a, card) != "blocked" {
		t.Fatal("valid input was lost")
	}
}

func TestCardStatusPromptAndSuccessDeliveryRetry(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	_, collector, _, _ := telegramFixture(t, a, card)
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Swap(false) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	t.Setenv("TELEGRAM_API_BASE", server.URL)
	prompt := telegramMessageUpdate(94001, 555, "/block_card")
	if code := telegramRequest(t, a, prompt); code != 500 {
		t.Fatal("expected delivery failure", code)
	}
	if code := telegramRequest(t, a, prompt); code != 200 {
		t.Fatal("prompt retry failed", code)
	}
	fail.Store(true)
	input := telegramMessageUpdate(94002, 555, "1234")
	if code := telegramRequest(t, a, input); code != 500 {
		t.Fatal("expected result delivery failure", code)
	}
	if cardState(t, a, card) != "blocked" {
		t.Fatal("change not committed")
	}
	if code := telegramRequest(t, a, input); code != 200 {
		t.Fatal("result retry failed", code)
	}
	var starts, changes int
	a.db.QueryRow("SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='card_status_input_start'", collector.ID).Scan(&starts)
	a.db.QueryRow("SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='card_status_command'", collector.ID).Scan(&changes)
	if starts != 1 || changes != 1 {
		t.Fatal("retry duplicated effect", starts, changes)
	}
}
