package main

import (
	"strings"
	"testing"
)

func TestTelegramBalanceSnapshotSurvivesLaterTransferAndDelivery(t *testing.T) {
	a, _, sender, recipient, fake := transferFixture(t)
	telegramRequest(t, a, telegramMessageUpdateInChat(91001, 556, 556, "private", "/start"))
	first := newTransfer(t, a, 91002, "20")
	confirmTransferSender(t, a, sender, first)
	if _, done := a.telegramRecipientTransfer(recipient, 556, 556, "receive:"+first); !done {
		t.Fatal("first receive")
	}
	second := newTransfer(t, a, 91003, "30")
	confirmTransferSender(t, a, sender, second)
	if _, done := a.telegramRecipientTransfer(recipient, 556, 556, "receive:"+second); !done {
		t.Fatal("second receive")
	}
	// Deliver the old result after both holders' balances have changed again.
	if err := a.deliverTransferNotifications(20); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		chat int64
		want string
	}{{telegramTestGroup, "80 ₽"}, {556, "20 ₽"}} {
		if err := a.telegramSyncCard(first, c.chat); err != nil {
			t.Fatal(err)
		}
		call := lastTelegramCall(t, fake, "editMessageText")
		if !strings.HasSuffix(str(call, "text"), "\nБаланс: "+c.want) {
			t.Fatal("old operation lost its balance or exposed the other holder's balance", call)
		}
	}
	assertTransferState(t, a, first, "posted", 0, 1)
	assertTransferState(t, a, second, "posted", 0, 1)
}

func TestTelegramHandoverFinalZeroBalance(t *testing.T) {
	a, _, sender, _, _, fake := handoverFixture(t)
	draft := newHandover(t, a, 92001, "100")
	if _, done := a.telegramCallback(sender, telegramTestGroup, uiCard(t, a, draft, telegramTestGroup), "confirm:"+draft); !done {
		t.Fatal("handover")
	}
	if !strings.HasSuffix(str(lastTelegramCall(t, fake, "editMessageText"), "text"), "\nБаланс: 0 ₽") {
		t.Fatal("zero omitted")
	}
}

func TestTelegramCardExpenseShowsCashBalance(t *testing.T) {
	a, _, _, _, fake := transferFixture(t)
	telegramRequest(t, a, telegramMessageUpdate(93001, 555, "/expense 1234 прогрев 1"))
	draft := uiDraft(t, a, 93001)
	telegramRequest(t, a, telegramButtonUpdate(93002, 555, "confirm", draft))
	if !strings.HasSuffix(str(lastTelegramCall(t, fake, "editMessageText"), "text"), "\nБаланс: 100 ₽") {
		t.Fatal("card expense changed displayed cash")
	}
}

func TestTelegramSummaryBalanceOnlyForPostedSnapshot(t *testing.T) {
	p := M{"telegram_actor_name": "Тест", "amount_cents": int64(100), "telegram_balance_after_cents": int64(-125)}
	text, _ := telegramOperationSummary("expense", "posted", p, 100, false)
	if !strings.HasSuffix(text, "\nБаланс: -1,25 ₽") {
		t.Fatal(text)
	}
	for _, status := range []string{"draft", "rejected", "reversed"} {
		text, _ := telegramOperationSummary("expense", status, p, 100, false)
		if strings.Contains(text, "Баланс:") {
			t.Fatal("non-posted summary shows obsolete snapshot", status, text)
		}
	}
	delete(p, "telegram_balance_after_cents")
	text, _ = telegramOperationSummary("expense", "posted", p, 100, false)
	if strings.Contains(text, "Баланс:") {
		t.Fatal("legacy operation presented as zero", text)
	}
}

func TestTelegramMoneySignedBalances(t *testing.T) {
	for _, c := range []struct {
		cents int64
		want  string
	}{{-1, "-0,01 ₽"}, {-100000, "-1 000 ₽"}, {-9223372036854775808, "-92 233 720 368 547 758,08 ₽"}, {0, "0 ₽"}, {144600000, "1 446 000 ₽"}} {
		if got := telegramMoney(c.cents); got != c.want {
			t.Fatalf("%d: %s != %s", c.cents, got, c.want)
		}
	}
}
