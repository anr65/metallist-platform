package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type telegramTransferRecipient struct {
	ID   string
	Name string
}

type telegramTransferDialog struct {
	Token      string
	Recipients []telegramTransferRecipient
	Recipient  telegramTransferRecipient
	Balance    int64
	Available  int64
}

func (a *App) telegramTransferRecipients(u telegramActor) ([]telegramTransferRecipient, error) {
	if u.Role != "collector" {
		return nil, errors.New("Перевод доступен только сборщику")
	}
	rows, err := a.db.Query(`SELECT c.id,c.name FROM custodians c
 LEFT JOIN LATERAL (
   SELECT max(j.posted_at) AS last_transfer FROM journal_entries j
   JOIN postings sent ON sent.entry_id=j.id AND sent.account='1200' AND sent.side='credit' AND sent.custodian_id=$1
   JOIN postings received ON received.entry_id=j.id AND received.account='1200' AND received.side='debit' AND received.custodian_id=c.id
   WHERE j.event_type='transfer'
 ) history ON true
 WHERE c.active AND c.kind='collector' AND c.id<>$1
 AND EXISTS (SELECT 1 FROM users u WHERE u.custodian_id=c.id AND u.active AND u.role='collector')
 ORDER BY history.last_transfer DESC NULLS LAST,c.name,c.id`, u.CustodianID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recipients []telegramTransferRecipient
	for rows.Next() {
		var recipient telegramTransferRecipient
		if err = rows.Scan(&recipient.ID, &recipient.Name); err != nil {
			return nil, err
		}
		recipients = append(recipients, recipient)
	}
	return recipients, rows.Err()
}

func (a *App) telegramTransferOptions(u telegramActor, chat int64) error {
	recipients, err := a.telegramTransferRecipients(u)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		_, err = a.db.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1", u.ID)
		if err != nil {
			return err
		}
		return a.telegramReply(chat, "Других активных сборщиков пока нет.", nil)
	}
	dialog := telegramTransferDialog{Token: id(), Recipients: recipients}
	raw, err := json.Marshal(dialog)
	if err != nil {
		return err
	}
	_, err = a.db.Exec("UPDATE telegram_dialogs SET context=$2 WHERE user_id=$1 AND command='transfer'", u.ID, raw)
	if err != nil {
		return err
	}
	buttons := []interface{}{}
	for i, recipient := range recipients {
		buttons = append(buttons, []interface{}{M{"text": recipient.Name, "callback_data": fmt.Sprintf("tr:%s:%d", dialog.Token, i)}})
	}
	return a.telegramReply(chat, "Выберите получателя перевода кнопкой. /cancel — отмена ввода.", M{"inline_keyboard": buttons})
}

func (a *App) telegramTransferDialog(u telegramActor) (telegramTransferDialog, error) {
	var raw []byte
	var dialog telegramTransferDialog
	err := a.db.QueryRow("SELECT context FROM telegram_dialogs WHERE user_id=$1 AND command='transfer' AND expires_at>now()", u.ID).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &dialog)
	}
	if err != nil || dialog.Token == "" || u.Role != "collector" {
		return dialog, errors.New("Этот ввод недоступен. Повторите /transfer")
	}
	return dialog, nil
}

func (a *App) telegramTransferRecipientActive(recipientID string) (string, error) {
	var name string
	err := a.db.QueryRow(`SELECT c.name FROM custodians c WHERE c.id=$1 AND c.active AND c.kind='collector'
 AND EXISTS (SELECT 1 FROM users u WHERE u.custodian_id=c.id AND u.active AND u.role='collector')`, recipientID).Scan(&name)
	if err != nil {
		return "", errors.New("Получатель недоступен. Повторите /transfer")
	}
	return name, nil
}

func (a *App) telegramTransferInputCallback(u telegramActor, chat, messageID, updateID int64, data string) (string, bool) {
	if strings.HasPrefix(data, "ta:") {
		var exists bool
		err := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM drafts WHERE idempotency_key=$1 AND created_by=$2 AND kind='transfer')", fmt.Sprintf("telegram:%d", updateID), u.ID).Scan(&exists)
		if err != nil {
			return "Не удалось открыть перевод", false
		}
		if exists {
			if err = a.telegramRetryPreview(updateID, chat); err != nil {
				return err.Error(), false
			}
			return "Перевод уже подготовлен", true
		}
	}
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return "Неизвестная кнопка", false
	}
	dialog, err := a.telegramTransferDialog(u)
	if err != nil || parts[1] != dialog.Token {
		return "Эта кнопка недоступна: только автор может продолжить активный ввод /transfer", false
	}
	if parts[0] == "tr" {
		choice, e := strconv.Atoi(parts[2])
		if e != nil || choice < 0 || choice >= len(dialog.Recipients) {
			return "Получатель недоступен", false
		}
		recipient := dialog.Recipients[choice]
		if dialog.Recipient.ID != "" && dialog.Recipient.ID != recipient.ID {
			return "Получатель уже выбран. Повторите /transfer", false
		}
		recipient.Name, err = a.telegramTransferRecipientActive(recipient.ID)
		if err != nil {
			return err.Error(), false
		}
		if dialog.Recipient.ID == "" {
			err = a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account='1200' AND custodian_id=$1", u.CustodianID).Scan(&dialog.Balance)
			if err != nil {
				return "Не удалось получить ваш баланс", false
			}
			err = a.db.QueryRow("SELECT $2::bigint-COALESCE(SUM(amount_cents),0) FROM cash_reservations WHERE custodian_id=$1 AND released_at IS NULL", u.CustodianID, dialog.Balance).Scan(&dialog.Available)
			if err != nil {
				return "Не удалось получить доступные наличные", false
			}
			dialog.Recipient = recipient
			raw, _ := json.Marshal(dialog)
			result, e := a.db.Exec("UPDATE telegram_dialogs SET context=$2,updated_at=now() WHERE user_id=$1 AND command='transfer' AND expires_at>now() AND context->>'Token'=$3 AND COALESCE(context->'Recipient'->>'ID','')=''", u.ID, raw, dialog.Token)
			if e != nil {
				return "Не удалось сохранить получателя", false
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return "Выбор уже изменился. Повторите /transfer", false
			}
		}
		var markup interface{}
		instruction := "Отправьте сумму сообщением, например: 1000,00. /cancel — отмена ввода."
		if dialog.Available > 0 {
			instruction = "Нажмите «Все наличные» или сразу отправьте сумму сообщением, например: 1000,00. /cancel — отмена ввода."
			markup = M{"inline_keyboard": []interface{}{[]interface{}{M{"text": "Все наличные: " + telegramMoney(dialog.Available), "callback_data": "ta:" + dialog.Token + ":all"}}}}
		}
		balanceText := telegramMoney(dialog.Balance)
		if dialog.Available != dialog.Balance {
			balanceText += "\nЗарезервировано: " + telegramMoney(dialog.Balance-dialog.Available) + "\nДоступно для перевода: " + telegramMoney(dialog.Available)
		}
		err = a.telegramEditMessageText(chat, messageID, "Получатель: "+dialog.Recipient.Name+"\nТекущий баланс наличных: "+balanceText+"\n\n"+instruction, markup)
		if err != nil {
			return err.Error(), false
		}
		return "Получатель выбран", false
	}
	if parts[0] != "ta" || parts[2] != "all" || dialog.Recipient.ID == "" || dialog.Available <= 0 {
		return "Эта сумма недоступна", false
	}
	message := &telegramMessage{Text: rub(dialog.Available)}
	message.Chat.ID = chat
	if err = a.db.QueryRow("SELECT telegram_id FROM users WHERE id=$1 AND active", u.ID).Scan(&message.From.ID); err != nil {
		return "Доступ отправителя изменился", false
	}
	if err = a.telegramHandleMessage(u, message, updateID, dialog.Token); err != nil {
		return err.Error(), false
	}
	return "Проверьте и подтвердите перевод", true
}
