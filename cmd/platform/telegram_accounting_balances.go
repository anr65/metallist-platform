package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (a *App) accountingBalances(u telegramActor, sender, chat int64, section string, page int) error {
	tx, err := a.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = accountingAuthorize(tx, u, sender, chat); err != nil {
		return a.telegramReply(chat, err.Error(), nil)
	}
	var asOf time.Time
	if err = tx.QueryRow("SELECT now()").Scan(&asOf); err != nil {
		return err
	}
	lines := []string{}
	title := ""
	var rows *sql.Rows
	switch section {
	case "cards":
		title = "💳 Все карты"
		rows, err = tx.Query(`SELECT c.id,b.name,c.mask,c.status,COALESCE((SELECT sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) FROM postings WHERE card_id=c.id AND account='1100'),0),o.observed_cents,o.observed_at
 FROM cards c JOIN banks b ON b.id=c.bank_id LEFT JOIN LATERAL(SELECT observed_cents,observed_at FROM observations WHERE card_id=c.id ORDER BY observed_at DESC,id DESC LIMIT 1)o ON true ORDER BY b.name,c.mask,c.id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid, bank, mask, status string
			var cents int64
			var observed sql.NullInt64
			var at sql.NullTime
			if err = rows.Scan(&cid, &bank, &mask, &status, &cents, &observed, &at); err != nil {
				rows.Close()
				return err
			}
			line := fmt.Sprintf("%s · %s · %s\nID: %s\nУчетный: %s", bank, mask, status, cid, accountingMoney(cents))
			if observed.Valid {
				line += fmt.Sprintf("\nФакт: %s (%s)\nРазница: %s", accountingMoney(observed.Int64), at.Time.In(a.location).Format("02.01.2006 15:04"), accountingMoney(cents-observed.Int64))
			} else {
				line += "\nФакт: нет данных"
			}
			lines = append(lines, line)
		}
	case "custodians":
		title = "👤 Хранители наличных"
		rows, err = tx.Query(`SELECT c.name,c.kind,c.active,COALESCE((SELECT sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) FROM postings WHERE custodian_id=c.id AND account=CASE WHEN c.kind='chief' THEN '1210' ELSE '1200' END),0),COALESCE((SELECT sum(amount_cents) FROM cash_reservations WHERE custodian_id=c.id AND released_at IS NULL),0) FROM custodians c ORDER BY c.name,c.id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name, kind string
			var active bool
			var cents, reserved int64
			if err = rows.Scan(&name, &kind, &active, &cents, &reserved); err != nil {
				rows.Close()
				return err
			}
			suffix := ""
			if !active {
				suffix = " · отключен"
			}
			lines = append(lines, fmt.Sprintf("%s (%s)%s\nНаличные: %s\nРезерв: %s\nДоступно: %s", name, kind, suffix, accountingMoney(cents), accountingMoney(reserved), accountingMoney(cents-reserved)))
		}
	case "merchants":
		title = "🏢 Мерчанты"
		rows, err = tx.Query(`SELECT m.name,COALESCE((SELECT sum(CASE WHEN side='credit' THEN amount_cents ELSE -amount_cents END) FROM postings WHERE merchant_id=m.id AND account='2100'),0),COALESCE((SELECT sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) FROM postings WHERE merchant_id=m.id AND account='1300'),0) FROM merchants m ORDER BY m.name,m.id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			var payable, receivable int64
			if err = rows.Scan(&name, &payable, &receivable); err != nil {
				rows.Close()
				return err
			}
			lines = append(lines, fmt.Sprintf("%s\nК возврату: %s\nДебиторка / переплата: %s", name, accountingMoney(payable), accountingMoney(receivable)))
		}
	default:
		section = "summary"
		title = "📊 Сводка балансов"
		var cards, cash, observed int64
		var count, total int
		err = tx.QueryRow(`SELECT
 COALESCE((SELECT sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) FROM postings WHERE account='1100'),0),
 COALESCE((SELECT sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) FROM postings WHERE account IN ('1200','1210')),0),
 COALESCE(sum(o.observed_cents),0),count(o.observed_cents),count(*) FROM cards c LEFT JOIN LATERAL(SELECT observed_cents FROM observations WHERE card_id=c.id ORDER BY observed_at DESC,id DESC LIMIT 1)o ON true`).Scan(&cards, &cash, &observed, &count, &total)
		if err != nil {
			return err
		}
		fact := "Нет данных"
		if count > 0 {
			fact = accountingMoney(observed)
		}
		lines = append(lines, fmt.Sprintf("Карты, учетный: %s\nКассы, учетный: %s\nОбщий учетный: %s\n\nКарты, последние фактические: %s\nДанные по %d из %d карт\nНаблюдения разных дат; не банковский баланс на текущий момент.", accountingMoney(cards), accountingMoney(cash), accountingMoney(cards+cash), fact, count, total))
	}
	if rows != nil {
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	pages := (len(lines) + 14) / 15
	if pages == 0 {
		pages = 1
	}
	if page < 0 || page >= pages {
		return a.telegramReply(chat, "Страница недоступна. Обновите /balances", accountingMenu())
	}
	first := page * 15
	last := first + 15
	if last > len(lines) {
		last = len(lines)
	}
	text := fmt.Sprintf("%s · %d/%d\nСнимок: %s\n\n", title, page+1, pages, asOf.In(a.location).Format("02.01.2006 15:04:05 МСК"))
	if len(lines) == 0 {
		text += "Нет записей"
	} else {
		text += strings.Join(lines[first:last], "\n\n")
	}
	menu := accountingMenu()
	buttons := []interface{}{}
	if page > 0 {
		buttons = append(buttons, M{"text": "← Назад", "callback_data": "ab:" + section + ":" + strconv.Itoa(page-1)})
	}
	buttons = append(buttons, M{"text": "Обновить", "callback_data": "ab:" + section + ":" + strconv.Itoa(page)})
	if page+1 < pages {
		buttons = append(buttons, M{"text": "Далее →", "callback_data": "ab:" + section + ":" + strconv.Itoa(page+1)})
	}
	menu["inline_keyboard"] = append(menu["inline_keyboard"].([]interface{}), buttons)
	if err = txAudit(tx, u.ID, "telegram", "accounting_balance_view", "balance", "", "success", "", M{"section": section, "page": page + 1}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Telegram text limit is in UTF-16 units. A conservative rune bound leaves room for emoji.
	chunks := []string{}
	chunk := ""
	for _, line := range strings.Split(text, "\n") {
		if len([]rune(chunk))+len([]rune(line)) > 3000 {
			chunks = append(chunks, chunk)
			chunk = ""
		}
		chunk += line + "\n"
	}
	if chunk != "" {
		chunks = append(chunks, chunk)
	}
	for i, c := range chunks {
		var markup interface{}
		if i == len(chunks)-1 {
			markup = menu
		}
		if err = a.telegramReply(chat, c, markup); err != nil {
			return err
		}
	}
	return nil
}
