package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type telegramCardStatusDialog struct {
	Token        string `json:"token"`
	ChatID       int64  `json:"chat_id"`
	LastUpdateID int64  `json:"last_update_id"`
	Selector     string `json:"selector"`
	Command      string `json:"-"`
}

func cardStatusSelectorValid(selector string) bool {
	return validID(selector) || len(selector) == 4 && strings.Trim(selector, "0123456789") == ""
}

func normalizeCardStatus(status string) string {
	switch strings.ToLower(status) {
	case "активный", "активная":
		return "active"
	case "заблокирована", "заблокирован", "заблокировать":
		return "blocked"
	}
	for key, label := range cardStatusLabels {
		if strings.EqualFold(status, label) {
			return key
		}
	}
	return strings.ToLower(status)
}

func cardStatusInputPrompt(d telegramCardStatusDialog) string {
	if d.Command == "block_card" {
		return "Какую карту заблокировать? Отправьте последние 4 цифры или ID карты, например: 1234. /cancel — отмена."
	}
	if d.Selector != "" {
		return "Выберите статус карты: Активна, Лимит, Блок или Архив. Отправьте только название статуса. /cancel — отмена."
	}
	return "Отправьте карту и статус одним сообщением, например: 1234 Блок или 1234 Лимит. Статусы: Активна, Лимит, Блок, Архив. Вместо последних 4 цифр можно указать ID карты. /cancel — отмена."
}

func (a *App) beginCardStatusInput(u telegramActor, message *telegramMessage, command, selector string, updateID int64, expected ...telegramCardStatusDialog) error {
	if selector != "" && !cardStatusSelectorValid(selector) {
		return errors.New("укажите последние четыре цифры или ID карты")
	}
	d := telegramCardStatusDialog{Token: id(), ChatID: message.Chat.ID, LastUpdateID: updateID, Selector: selector, Command: command}
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(expected) > 0 {
		var currentToken string
		err = tx.QueryRow("SELECT context->>'token' FROM telegram_dialogs WHERE user_id=$1 AND command=$2 AND expires_at>now() FOR UPDATE", u.ID, expected[0].Command).Scan(&currentToken)
		if err != nil || currentToken != expected[0].Token {
			return errors.New("Ввод уже изменился. Повторите команду")
		}
	}
	_, err = tx.Exec(`INSERT INTO telegram_dialogs(user_id,command,context,expires_at) VALUES($1,$2,$3,now()+interval '10 minutes')
 ON CONFLICT(user_id) DO UPDATE SET command=EXCLUDED.command,context=EXCLUDED.context,expires_at=EXCLUDED.expires_at,updated_at=now()`, u.ID, command, encode(d))
	if err != nil {
		return err
	}
	if err = txAudit(tx, u.ID, "telegram", "card_status_input_start", "command", "", "success", "", M{"update_id": updateID, "command": command}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return a.telegramReply(message.Chat.ID, cardStatusInputPrompt(d), nil)
}

func (a *App) cardStatusDialog(u telegramActor, chat int64) (telegramCardStatusDialog, error) {
	var d telegramCardStatusDialog
	var raw []byte
	err := a.db.QueryRow("SELECT command,context FROM telegram_dialogs WHERE user_id=$1 AND command IN ('card_status','block_card') AND expires_at>now()", u.ID).Scan(&d.Command, &raw)
	if err != nil {
		return d, err
	}
	if err = json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	if d.Token == "" || d.ChatID != chat {
		return d, sql.ErrNoRows
	}
	return d, nil
}

// Only a slash command or the same actor's live dialog may change a card.
func (a *App) telegramCardStatusInput(u telegramActor, message *telegramMessage, updateID int64, command string) error {
	fields := strings.Fields(message.Text)
	var expected []telegramCardStatusDialog
	if command != "" {
		fields = fields[1:]
	} else {
		d, err := a.cardStatusDialog(u, message.Chat.ID)
		if err != nil {
			return errors.New("Ввод завершён. Выберите /card_status или /block_card заново")
		}
		if updateID <= d.LastUpdateID {
			return errors.New("Сообщение устарело. Повторите команду")
		}
		command = d.Command
		expected = append(expected, d)
		if d.Selector != "" {
			fields = append([]string{d.Selector}, fields...)
		}
	}
	// A group reply may mention colleagues after the actual parameters.
	for len(fields) > 0 && strings.HasPrefix(fields[len(fields)-1], "@") {
		fields = fields[:len(fields)-1]
	}
	if len(fields) == 0 || command == "card_status" && len(fields) == 1 && cardStatusSelectorValid(fields[0]) {
		selector := ""
		if len(fields) == 1 {
			selector = fields[0]
		}
		return a.beginCardStatusInput(u, message, command, selector, updateID, expected...)
	}
	if command == "block_card" && len(fields) == 2 && normalizeCardStatus(fields[1]) == "blocked" {
		fields = fields[:1]
	}
	words := append([]string{"/" + command}, fields...)
	if err := a.telegramCardStatus(u, words, command, updateID, expected...); err != nil {
		return err
	}
	return a.cardStatusReply(u, message.Chat.ID, updateID)
}

func (a *App) cardStatusReply(u telegramActor, chat, updateID int64) error {
	var suffix, status string
	err := a.db.QueryRow(`SELECT c.last4,a.detail->>'status' FROM audit_events a JOIN cards c ON c.id=a.object_id
 WHERE a.actor_id=$1 AND a.channel='telegram' AND a.action='card_status_command' AND a.outcome='success' AND a.detail->>'update_id'=$2 LIMIT 1`, u.ID, fmt.Sprint(updateID)).Scan(&suffix, &status)
	if err != nil {
		return err
	}
	return a.telegramReply(chat, fmt.Sprintf("Карта ••••%s: %s.", suffix, cardStatusLabels[status]), nil)
}

func (a *App) retryCardStatusReply(u telegramActor, message *telegramMessage, updateID int64) (bool, error) {
	if message == nil {
		return false, nil
	}
	var done bool
	err := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM audit_events WHERE actor_id=$1 AND action='card_status_command' AND channel='telegram' AND detail->>'update_id'=$2)", u.ID, fmt.Sprint(updateID)).Scan(&done)
	if err != nil {
		return true, err
	}
	if done {
		return true, a.cardStatusReply(u, message.Chat.ID, updateID)
	}
	d, err := a.cardStatusDialog(u, message.Chat.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return true, err
	}
	if err == nil && d.LastUpdateID == updateID {
		return true, a.telegramReply(message.Chat.ID, cardStatusInputPrompt(d), nil)
	}
	return false, nil
}
