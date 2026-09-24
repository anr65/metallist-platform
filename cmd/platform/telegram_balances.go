package main

import (
	"errors"
	"fmt"
	"strings"
)

// telegramCardsBalance returns the latest factual observation for each card.
// It intentionally never substitutes a ledger amount for a missing observation.
func (a *App) telegramCardsBalance(u telegramActor, chatID, updateID int64) error {
	rows, err := a.db.Query(`SELECT c.mask,o.observed_cents
	FROM cards c LEFT JOIN LATERAL (
		SELECT observed_cents FROM observations WHERE card_id=c.id ORDER BY observed_at DESC,id DESC LIMIT 1
	) o ON true
	ORDER BY c.status='active' DESC, c.mask, c.id`)
	if err != nil {
		return errors.New("Не удалось получить балансы карт")
	}
	defer rows.Close()

	var total int64
	var observedCount, cardCount int
	lines := []string{"💳 Фактические балансы карт"}
	for rows.Next() {
		var mask string
		var cents *int64
		if err = rows.Scan(&mask, &cents); err != nil {
			return errors.New("Не удалось прочитать балансы карт")
		}
		cardCount++
		if cents == nil {
			lines = append(lines, fmt.Sprintf("• %s — фактический остаток не зафиксирован", mask))
			continue
		}
		observedCount++
		total += *cents
		lines = append(lines, fmt.Sprintf("• %s — %s", mask, telegramMoney(*cents)))
	}
	if err = rows.Err(); err != nil {
		return errors.New("Не удалось получить балансы карт")
	}
	summary := "Фактический итог: не зафиксирован"
	if observedCount == cardCount {
		summary = "Общий фактический баланс: " + telegramMoney(total)
	} else if observedCount > 0 {
		summary = fmt.Sprintf("Фактический итог по %d из %d карт: %s", observedCount, cardCount, telegramMoney(total))
	}
	lines = append([]string{lines[0], "", summary, ""}, lines[1:]...)
	a.logAudit(u.ID, "telegram", "cards_balance_view", "balance", "", "success", "", M{"update_id": updateID})
	return a.telegramReply(chatID, strings.Join(lines, "\n"), nil)
}

func (a *App) telegramMyBalance(u telegramActor, chatID, updateID int64) error {
	var cents int64
	err := a.db.QueryRow(`SELECT COALESCE(SUM(CASE WHEN p.side='debit' THEN p.amount_cents ELSE -p.amount_cents END),0)
		FROM postings p JOIN custodians c ON c.id=p.custodian_id
		WHERE p.custodian_id=$1 AND p.account=CASE WHEN c.kind='chief' THEN '1210' ELSE '1200' END`, u.CustodianID).Scan(&cents)
	if err != nil {
		return errors.New("Не удалось получить ваш баланс")
	}
	a.logAudit(u.ID, "telegram", "own_cash_balance_view", "custodian", u.CustodianID, "success", "", M{"update_id": updateID})
	return a.telegramReply(chatID, "👤 Ваш баланс наличных\n\n"+telegramMoney(cents), nil)
}
