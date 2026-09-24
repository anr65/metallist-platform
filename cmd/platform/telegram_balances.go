package main

import (
	"errors"
	"fmt"
	"strings"
)

// telegramCardsBalance reads the card asset account only. It neither treats an
// observed balance as a posting nor exposes a full card number.
func (a *App) telegramCardsBalance(u telegramActor, chatID, updateID int64) error {
	rows, err := a.db.Query(`WITH balances AS (
		SELECT card_id, SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) AS cents
		FROM postings WHERE account='1100' AND card_id IS NOT NULL GROUP BY card_id
	)
	SELECT c.mask, COALESCE(b.cents,0)
	FROM cards c LEFT JOIN balances b ON b.card_id=c.id
	ORDER BY c.status='active' DESC, c.mask, c.id`)
	if err != nil {
		return errors.New("Не удалось получить балансы карт")
	}
	defer rows.Close()

	var total int64
	lines := []string{"💳 Балансы карт"}
	for rows.Next() {
		var mask string
		var cents int64
		if err = rows.Scan(&mask, &cents); err != nil {
			return errors.New("Не удалось прочитать балансы карт")
		}
		total += cents
		lines = append(lines, fmt.Sprintf("• %s — %s", mask, telegramMoney(cents)))
	}
	if err = rows.Err(); err != nil {
		return errors.New("Не удалось получить балансы карт")
	}
	lines = append([]string{lines[0], "", "Общий баланс: " + telegramMoney(total), ""}, lines[1:]...)
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
