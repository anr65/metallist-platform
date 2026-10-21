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
	status := "blocked"
	if command == "card_status" {
		if len(words) != 3 {
			return errors.New("Формат: /card_status последние4 active|limit|blocked|retired (Активна|Лимит|Блок|Архив). Вместо последних4 можно указать ID карты")
		}
		status = normalizeCardStatus(words[2])
	} else if len(words) != 2 {
		return errors.New("Формат: /block_card последние4 или ID карты")
	}
	if _, ok := cardStatusLabels[status]; !ok {
		return errors.New("неверный статус карты")
	}
	selector := words[1]
	if !cardStatusSelectorValid(selector) {
		return errors.New("укажите последние четыре цифры или ID карты")
	}
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	query := "SELECT id FROM cards WHERE last4=$1 ORDER BY id FOR UPDATE"
	if validID(selector) {
		query = "SELECT id FROM cards WHERE id=$1 FOR UPDATE"
	}
	rows, err := tx.Query(query, selector)
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
		return errors.New("карта не найдена")
	}
	if len(ids) > 1 {
		candidates := ids
		if len(candidates) > 20 {
			candidates = candidates[:20]
		}
		return errors.New("Последние цифры неоднозначны. Повторите команду с ID нужной карты вместо последних цифр:\n" + strings.Join(candidates, "\n"))
	}
	if err = changeCardStatus(tx, u.ID, "telegram", ids[0], status, time.Now()); err != nil {
		return err
	}
	if err = txAudit(tx, u.ID, "telegram", "card_status_command", "card", ids[0], "success", "", M{"update_id": updateID, "status": status}); err != nil {
		return err
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
