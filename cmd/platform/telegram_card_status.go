package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type telegramCardStatusDialog struct {
	PromptMessageID         int64  `json:"prompt_message_id"`
	PreviousPromptMessageID int64  `json:"previous_prompt_message_id"`
	Token                   string `json:"token"`
	ChatID                  int64  `json:"chat_id"`
	LastUpdateID            int64  `json:"last_update_id"`
	Selector                string `json:"selector"`
	Command                 string `json:"-"`
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

type telegramCardStatusItem struct {
	Selector string
	Status   string
}

func cardStatusSelectorsValid(fields []string) bool {
	if len(fields) == 0 || len(fields) > 20 {
		return false
	}
	for _, field := range fields {
		if !cardStatusSelectorValid(field) {
			return false
		}
	}
	return true
}

func telegramCardStatusItems(fields []string, command string) ([]telegramCardStatusItem, error) {
	status := "blocked"
	if command == "card_status" {
		if len(fields) < 2 {
			return nil, errors.New("укажите карты и статус: Активна, Лимит, Блок или Архив")
		}
		status = normalizeCardStatus(fields[len(fields)-1])
		if _, ok := cardStatusLabels[status]; !ok {
			return nil, errors.New("неверный статус карты")
		}
		fields = fields[:len(fields)-1]
	}
	items := []telegramCardStatusItem{}
	seen := map[string]string{}
	for i := 0; i < len(fields); i++ {
		selector := strings.ToLower(fields[i])
		if !cardStatusSelectorValid(selector) {
			return nil, errors.New("укажите последние четыре цифры или ID карты, по одной карте в строке")
		}
		itemStatus := status
		if command == "card_status" && i+1 < len(fields) {
			explicit := normalizeCardStatus(fields[i+1])
			if _, ok := cardStatusLabels[explicit]; ok {
				itemStatus = explicit
				i++
			}
		}
		if previous, ok := seen[selector]; ok {
			if previous != itemStatus {
				return nil, errors.New("для одной карты указаны разные статусы")
			}
			continue
		}
		seen[selector] = itemStatus
		items = append(items, telegramCardStatusItem{selector, itemStatus})
		if len(items) > 20 {
			return nil, errors.New("за одно сообщение можно изменить не более 20 карт")
		}
	}
	if len(items) == 0 {
		return nil, errors.New("укажите хотя бы одну карту")
	}
	return items, nil
}

func cardStatusInputPrompt(d telegramCardStatusDialog) string {
	if d.Command == "block_card" {
		return "Какие карты заблокировать? Отправьте последние 4 цифры или ID карт по одной в строке (до 20):\n1234\n0802\n/cancel — отмена."
	}
	if d.Selector != "" {
		return "Выберите статус для выбранных карт: Активна, Лимит, Блок или Архив. Отправьте только название статуса. /cancel — отмена."
	}
	return "Отправьте карты по одной в строке (до 20) и статус в конце списка, например:\n1234\n0802 Лимит\nСтатусы: Активна, Лимит, Блок, Архив. Можно указать статус у каждой карты или отправить сначала список, затем статус отдельным сообщением. Вместо последних 4 цифр можно указать ID карты. /cancel — отмена."
}

func (a *App) beginCardStatusInput(u telegramActor, message *telegramMessage, command, selector string, updateID int64, expected ...telegramCardStatusDialog) error {
	if selector != "" && !cardStatusSelectorsValid(strings.Fields(selector)) {
		return errors.New("укажите последние четыре цифры или ID карты")
	}
	d := telegramCardStatusDialog{Token: id(), ChatID: message.Chat.ID, LastUpdateID: updateID, Selector: selector, Command: command}
	if len(expected) > 0 {
		d.PreviousPromptMessageID = expected[0].PromptMessageID
	}
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
	return a.telegramCardStatusPrompt(u, d)
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
			fields = append(strings.Fields(d.Selector), fields...)
		}
	}
	// A group reply may mention colleagues after the actual parameters.
	for len(fields) > 0 && strings.HasPrefix(fields[len(fields)-1], "@") {
		fields = fields[:len(fields)-1]
	}
	if len(fields) == 0 || command == "card_status" && cardStatusSelectorsValid(fields) {
		selector := ""
		if len(fields) > 0 {
			selector = strings.Join(fields, " ")
		}
		return a.beginCardStatusInput(u, message, command, selector, updateID, expected...)
	}
	if command == "block_card" && len(fields) >= 2 && normalizeCardStatus(fields[len(fields)-1]) == "blocked" {
		fields = fields[:len(fields)-1]
	}
	words := append([]string{"/" + command}, fields...)
	if err := a.telegramCardStatus(u, words, command, updateID, expected...); err != nil {
		return err
	}
	return a.cardStatusReply(u, message.Chat.ID, updateID)
}

func (a *App) cardStatusReply(u telegramActor, chat, updateID int64) error {
	var prompt, storedChat int64
	rows, err := a.db.Query(`SELECT c.last4,a.detail->>'status',COALESCE((a.detail->>'prompt_message_id')::bigint,0),COALESCE((a.detail->>'prompt_chat_id')::bigint,0) FROM audit_events a JOIN cards c ON c.id=a.object_id
 WHERE a.actor_id=$1 AND a.channel='telegram' AND a.action='card_status_command' AND a.outcome='success' AND a.detail->>'update_id'=$2 ORDER BY COALESCE((a.detail->>'batch_index')::int,0),a.object_id`, u.ID, fmt.Sprint(updateID))
	if err != nil {
		return err
	}
	replies := []string{}
	for rows.Next() {
		var suffix, status string
		if err = rows.Scan(&suffix, &status, &prompt, &storedChat); err != nil {
			rows.Close()
			return err
		}
		replies = append(replies, fmt.Sprintf("Карта ••••%s: %s.", suffix, cardStatusLabels[status]))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(replies) == 0 {
		return sql.ErrNoRows
	}
	if err = a.telegramReply(chat, strings.Join(replies, "\n"), nil); err != nil {
		return err
	}
	if prompt <= 0 || storedChat != chat {
		return nil
	}
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = queueTelegramPromptDeletion(tx, chat, prompt); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_ = a.telegramCleanupPrompts(10)
	return nil
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
		return true, a.telegramCardStatusPrompt(u, d)
	}
	return false, nil
}

func (a *App) telegramCardStatusPrompt(u telegramActor, d telegramCardStatusDialog) error {
	if d.PromptMessageID > 0 {
		return a.telegramEditMessageText(d.ChatID, d.PromptMessageID, cardStatusInputPrompt(d), telegramEmptyKeyboard())
	}
	sent, err := a.telegramSendMessage(d.ChatID, cardStatusInputPrompt(d), nil)
	if err != nil {
		return err
	}
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE telegram_dialogs SET context=jsonb_set(context,'{prompt_message_id}',to_jsonb($2::bigint)) WHERE user_id=$1 AND context->>'token'=$3 AND expires_at>now()", u.ID, sent, d.Token)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Ввод уже изменился. Повторите команду")
	}
	if err = queueTelegramPromptDeletion(tx, d.ChatID, d.PreviousPromptMessageID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_ = a.telegramCleanupPrompts(10)
	return nil
}
