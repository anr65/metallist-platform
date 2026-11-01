package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

var cardStatusLabels = map[string]string{"active": "Активна", "limit": "Лимит", "blocked": "Блок", "retired": "Архив"}

func nextCardMonth(now time.Time) time.Time {
	loc, _ := time.LoadLocation("Europe/Moscow")
	local := now.In(loc)
	return time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, loc)
}

// Locks the card before changing it. Repeating a status does not extend a monthly limit.
func changeCardStatus(tx *sql.Tx, actor, channel, cardID, status string, now time.Time) error {
	if _, ok := cardStatusLabels[status]; !ok {
		return errors.New("неверный статус карты")
	}
	var previous string
	if err := tx.QueryRow("SELECT status FROM cards WHERE id=$1 FOR UPDATE", cardID).Scan(&previous); err != nil {
		return errors.New("карта не найдена")
	}
	if previous == status {
		return nil
	}
	var until interface{}
	if status == "limit" {
		until = nextCardMonth(now)
	}
	if _, err := tx.Exec("UPDATE cards SET status=$2,limit_until=$3 WHERE id=$1", cardID, status, until); err != nil {
		return err
	}
	return txAudit(tx, actor, channel, "card_status_change", "card", cardID, "success", "", M{"before": previous, "after": status})
}

func (a *App) resetCardLimits(now time.Time) error {
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT id FROM cards WHERE status='limit' AND limit_until <= $1 ORDER BY id FOR UPDATE", now)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var cardID string
		if err = rows.Scan(&cardID); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, cardID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, cardID := range ids {
		if err = changeCardStatus(tx, "", "system", cardID, "active", now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) cardLimitWorker() {
	for {
		if err := a.resetCardLimits(time.Now()); err != nil {
			log.Printf("card monthly reset failed: %v", err)
		}
		time.Sleep(time.Minute)
	}
}

func (a *App) telegramCardStatus(u telegramActor, words []string, command string, updateID int64, expected ...telegramCardStatusDialog) error {
	if u.Role != "chief" && u.Role != "collector" && u.Role != "operator" {
		return errors.New("смена статуса недоступна")
	}
	if len(words) == 0 {
		return errors.New("укажите карты")
	}
	items, err := telegramCardStatusItems(words[1:], command)
	if err != nil {
		return err
	}
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prompt, promptChat int64
	if len(expected) > 0 {
		prompt, promptChat = expected[0].PromptMessageID, expected[0].ChatID
	} else {
		err = tx.QueryRow("SELECT COALESCE((context->>'prompt_message_id')::bigint,0),COALESCE((context->>'chat_id')::bigint,0) FROM telegram_dialogs WHERE user_id=$1 AND command IN ('card_status','block_card')", u.ID).Scan(&prompt, &promptChat)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if len(expected) > 0 {
		d := expected[0]
		res, e := tx.Exec(`DELETE FROM telegram_dialogs WHERE user_id=$1 AND command=$2 AND expires_at>now()
         AND context->>'token'=$3 AND context->>'chat_id'=$4 AND (context->>'last_update_id')::bigint<$5`, u.ID, d.Command, d.Token, fmt.Sprint(d.ChatID), updateID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return errors.New("Ввод уже изменился. Повторите команду")
		}
	} else {
		if _, e := tx.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1 AND command IN ('card_status','block_card')", u.ID); e != nil {
			return e
		}
	}
	resolved := map[string]string{}
	now := time.Now()
	for index, item := range items {
		query := "SELECT id FROM cards WHERE last4=$1 ORDER BY id FOR UPDATE"
		if validID(item.Selector) {
			query = "SELECT id FROM cards WHERE id=$1 FOR UPDATE"
		}
		rows, err := tx.Query(query, item.Selector)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var cardID string
			if err = rows.Scan(&cardID); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, cardID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return fmt.Errorf("карта %s не найдена. Ни одна карта не изменена", item.Selector)
		}
		if len(ids) > 1 {
			candidates := ids
			if len(candidates) > 20 {
				candidates = candidates[:20]
			}
			return errors.New("Последние цифры " + item.Selector + " неоднозначны. Ни одна карта не изменена. Повторите список с ID нужной карты:\n" + strings.Join(candidates, "\n"))
		}
		if previous, ok := resolved[ids[0]]; ok {
			if previous != item.Status {
				return errors.New("для одной карты указаны разные статусы. Ни одна карта не изменена")
			}
			continue
		}
		resolved[ids[0]] = item.Status
		if err = changeCardStatus(tx, u.ID, "telegram", ids[0], item.Status, now); err != nil {
			return err
		}
		if err = txAudit(tx, u.ID, "telegram", "card_status_command", "card", ids[0], "success", "", M{"update_id": updateID, "status": item.Status, "batch_index": index, "command": command, "prompt_message_id": prompt, "prompt_chat_id": promptChat}); err != nil {
			return err
		}

	}
	return tx.Commit()
}

func validateDropHandler(tx *sql.Tx, handlerID string) error {
	if !validID(handlerID) {
		return errors.New("выберите дроповода")
	}
	var active bool
	if err := tx.QueryRow("SELECT active FROM drop_handlers WHERE id=$1 FOR SHARE", handlerID).Scan(&active); err != nil || !active {
		return errors.New("выберите действующего дроповода")
	}
	return nil
}
