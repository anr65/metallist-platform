package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const telegramBalancePageSize = 20

type telegramCardBalance struct {
	Mask     string
	Observed *int64
}

// telegramCardsBalance returns the latest factual observation for each card.
// It intentionally never substitutes a ledger amount for a missing observation.
func (a *App) telegramCardsBalance(u telegramActor, chatID, updateID int64) error {
	return a.telegramCardsBalancePage(u, chatID, 0, 0, updateID)
}

func (a *App) telegramCardsBalancePage(u telegramActor, chatID, messageID int64, page int, updateID int64) error {
	rows, err := a.db.Query(`SELECT c.mask,o.observed_cents,g.balance_card_id::text
	FROM cards c JOIN card_balance_groups g ON g.card_id=c.id LEFT JOIN shared_card_observations o ON o.card_id=c.id
	ORDER BY c.status='active' DESC, c.mask, c.id`)
	if err != nil {
		return errors.New("Не удалось получить балансы карт")
	}
	defer rows.Close()

	cards := []telegramCardBalance{}
	var total int64
	var observedCount int
	groups := map[string]bool{}
	observedGroups := map[string]bool{}
	for rows.Next() {
		var mask, group string
		var cents *int64
		if err = rows.Scan(&mask, &cents, &group); err != nil {
			return errors.New("Не удалось прочитать балансы карт")
		}
		cards = append(cards, telegramCardBalance{Mask: mask, Observed: cents})
		groups[group] = true
		if cents != nil && !observedGroups[group] {
			observedGroups[group] = true
			observedCount++
			total += *cents
		}
	}
	if err = rows.Err(); err != nil {
		return errors.New("Не удалось получить балансы карт")
	}
	pageCount := (len(cards) + telegramBalancePageSize - 1) / telegramBalancePageSize
	if pageCount == 0 {
		pageCount = 1
	}
	if page < 0 || page >= pageCount {
		return errors.New("Эта страница балансов недоступна")
	}
	summary := "Фактический итог: не зафиксирован"
	if observedCount == len(groups) {
		summary = "Общий фактический баланс: " + telegramMoney(total)
	} else if observedCount > 0 {
		summary = fmt.Sprintf("Фактический итог по %d из %d карт: %s", observedCount, len(groups), telegramMoney(total))
	}
	first := page * telegramBalancePageSize
	last := first + telegramBalancePageSize
	if last > len(cards) {
		last = len(cards)
	}
	title := "💳 Фактические балансы карт"
	if pageCount > 1 {
		title += fmt.Sprintf(" · страница %d из %d", page+1, pageCount)
	}
	lines := []string{title, "", summary, ""}
	for _, card := range cards[first:last] {
		if card.Observed == nil {
			lines = append(lines, fmt.Sprintf("• %s — фактический остаток не зафиксирован", card.Mask))
		} else {
			lines = append(lines, fmt.Sprintf("• %s — %s", card.Mask, telegramMoney(*card.Observed)))
		}
	}
	buttons := []interface{}{}
	if page > 0 {
		buttons = append(buttons, M{"text": "← Назад", "callback_data": "cards:" + strconv.Itoa(page-1)})
	}
	if page+1 < pageCount {
		buttons = append(buttons, M{"text": "Далее →", "callback_data": "cards:" + strconv.Itoa(page+1)})
	}
	var markup interface{}
	if len(buttons) > 0 {
		markup = M{"inline_keyboard": []interface{}{buttons}}
	}
	detail := M{"page": page + 1, "page_count": pageCount}
	if updateID > 0 {
		detail["update_id"] = updateID
	}
	a.logAudit(u.ID, "telegram", "cards_balance_view", "balance", "", "success", "", detail)
	message := strings.Join(lines, "\n")
	if messageID > 0 {
		return a.telegramEditMessageText(chatID, messageID, message, markup)
	}
	return a.telegramReply(chatID, message, markup)
}

func (a *App) telegramMyBalance(u telegramActor, chatID, updateID int64) error {
	var cents, reserved int64
	err := a.db.QueryRow(`SELECT COALESCE(SUM(CASE WHEN p.side='debit' THEN p.amount_cents ELSE -p.amount_cents END),0),
        (SELECT COALESCE(sum(amount_cents),0) FROM cash_reservations WHERE custodian_id=$1 AND released_at IS NULL)
		FROM postings p JOIN custodians c ON c.id=p.custodian_id
		WHERE p.custodian_id=$1 AND p.account=CASE WHEN c.kind='chief' THEN '1210' ELSE '1200' END`, u.CustodianID).Scan(&cents, &reserved)
	if err != nil {
		return errors.New("Не удалось получить ваш баланс")
	}
	a.logAudit(u.ID, "telegram", "own_cash_balance_view", "custodian", u.CustodianID, "success", "", M{"update_id": updateID})
	return a.telegramReply(chatID, "👤 Ваш баланс наличных\n\n"+telegramMoney(cents)+"\nЗарезервировано: "+telegramMoney(reserved)+"\nДоступно: "+telegramMoney(cents-reserved), nil)
}
