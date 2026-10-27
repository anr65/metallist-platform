package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const telegramRenderedStateSQL = `d.status || ':' || COALESCE(d.payload->>'telegram_sender_confirmed','false') || ':' || COALESCE(d.confirmed_amount_cents::text,'')`

func telegramEmptyKeyboard() M { return M{"inline_keyboard": []interface{}{}} }

// Caller holds the draft lock. Persist the canonical card, so the outbox edits
// it after a restart rather than appending another lifecycle message.
func (a *App) telegramPublishCard(tx *sql.Tx, draftID string, chat int64, text string, markup interface{}) error {
	var messageID int64
	err := tx.QueryRow("SELECT message_id FROM telegram_operation_messages WHERE draft_id=$1 AND chat_id=$2", draftID, chat).Scan(&messageID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if markup == nil {
		markup = telegramEmptyKeyboard()
	}
	if messageID > 0 {
		err = a.telegramEditMessageText(chat, messageID, text, markup)
	} else {
		messageID, err = a.telegramSendMessage(chat, text, markup)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO telegram_operation_messages(draft_id,chat_id,message_id,rendered_state)
 SELECT d.id,$2,$3,`+telegramRenderedStateSQL+` FROM drafts d WHERE d.id=$1
 ON CONFLICT(draft_id,chat_id) DO UPDATE SET rendered_state=EXCLUDED.rendered_state,last_error='',next_attempt_at=now()`, draftID, chat, messageID)
	return err
}

// Snapshot in the posting transaction, never at delivery time. A retry must
// describe the same operation even after the custodian's next transaction.
func telegramCaptureBalance(tx *sql.Tx, kind string, p M, creator string) error {
	if p["telegram_confirmation_required"] != true && p["telegram_private_repayment"] != true {
		return nil
	}
	custodian := ""
	switch kind {
	case "withdrawal":
		custodian = str(p, "custodian_id")
	case "handover", "transfer":
		custodian = str(p, "from_custodian_id")
	case "repayment":
		custodian = str(p, "source_id")
	case "expense":
		if err := tx.QueryRow("SELECT custodian_id::text FROM users WHERE id=$1", creator).Scan(&custodian); err != nil {
			return err
		}
	default:
		return nil
	}
	read := func(holder string) (int64, error) {
		if holder == "" {
			return 0, errors.New("Не указан хранитель наличных")
		}
		var cents int64
		err := tx.QueryRow(`SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0)
 FROM postings WHERE account IN ('1200','1210') AND custodian_id=$1`, holder).Scan(&cents)
		return cents, err
	}
	cents, err := read(custodian)
	if err != nil {
		return err
	}
	p["telegram_balance_after_cents"] = cents
	if kind == "transfer" {
		cents, err = read(str(p, "to_custodian_id"))
		if err != nil {
			return err
		}
		p["telegram_recipient_balance_after_cents"] = cents
	}
	return nil
}

func telegramOperationSummary(kind, status string, p M, confirmed int64, private bool) (string, interface{}) {
	text, markup := telegramOperationSummaryText(kind, status, p, confirmed, private)
	if status == "posted" && text != "" {
		key := "telegram_balance_after_cents"
		if kind == "transfer" && private {
			key = "telegram_recipient_balance_after_cents"
		}
		if value, exists := p[key]; exists {
			if cents, err := telegramInt(value); err == nil {
				text += "\nБаланс: " + telegramMoney(cents)
			}
		}
	}
	return text, markup
}

func telegramOperationSummaryText(kind, status string, p M, confirmed int64, private bool) (string, interface{}) {
	amount, _ := telegramInt(p["amount_cents"])
	actor := str(p, "telegram_actor_name")
	prefix := "✅ "
	if status == "rejected" {
		prefix = "❌ "
	} else if status == "reversed" {
		prefix = "↩️ "
	} else if status == "draft" {
		prefix = "⏳ "
	}
	switch kind {
	case "withdrawal":
		items, _ := telegramWithdrawalItems(p)
		text := prefix + actor + " · снято " + telegramMoney(amount)
		if len(items) == 1 {
			text += " · " + str(items[0], "telegram_mask")
		} else {
			text += " · " + telegramWithdrawalCount(len(items))
		}
		if status == "rejected" {
			text = prefix + actor + " · снятие отклонено · " + telegramMoney(amount)
		} else if status == "reversed" {
			text = prefix + actor + " · снятие сторнировано · " + telegramMoney(amount)
		}
		var fees int64
		for _, item := range items {
			fee, _ := telegramWithdrawalFee(item)
			fees += fee
		}
		if fees > 0 && status != "rejected" {
			text += "\nБанковская комиссия: " + telegramMoney(fees)
		}
		return text, nil
	case "expense":
		items, _ := telegramExpenseItems(p)
		label := "расход"
		if len(items) == 1 {
			label = telegramCategoryName(str(items[0], "category"))
		} else {
			label = fmt.Sprintf("расходы: %d", len(items))
		}
		if status == "rejected" {
			label += " · отклонено"
		} else if status == "reversed" {
			label += " · сторнировано"
		}
		return prefix + actor + " · " + label + " · " + telegramMoney(amount), nil
	case "handover":
		text := prefix + actor + " → " + str(p, "to_custodian_name") + " · " + telegramMoney(amount)
		switch status {
		case "draft":
			text += "\nОжидает главного администратора. Наличные остаются у отправителя."
		case "posted":
			text = prefix + actor + " → " + str(p, "to_custodian_name") + " · принято " + telegramMoney(confirmed)
			if confirmed != amount {
				text += " из " + telegramMoney(amount)
			}
		case "rejected":
			text += " · отклонено"
		case "reversed":
			text += " · сторнировано"
		}
		return text, nil
	case "transfer":
		text := prefix + actor + " → " + str(p, "to_custodian_name") + " · " + telegramMoney(amount)
		switch status {
		case "draft":
			text += "\nОжидает получателя · сумма зарезервирована"
			if !private {
				return text, M{"inline_keyboard": []interface{}{[]interface{}{M{"text": "Отменить перевод", "callback_data": "reject:" + str(p, "presentation_draft_id")}}}}
			}
		case "posted":
			text += " · получено"
		case "rejected":
			text += " · отменён, резерв освобождён"
		case "reversed":
			text += " · сторнировано"
		}
		return text, nil
	case "repayment":
		label := "выдано"
		if status == "rejected" {
			label = "выдача отменена"
		} else if status == "reversed" {
			label = "выдача сторнирована"
		}
		return prefix + actor + " → " + str(p, "merchant_name") + " · " + label + " " + telegramMoney(amount), nil
	}
	return "", nil
}

// No financial commands here: only reconstruct delivery from committed state.
func (a *App) telegramSyncCard(draftID string, chat int64) error {
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind, status string
	var raw []byte
	var confirmed int64
	if err = tx.QueryRow("SELECT kind,status,payload,COALESCE(confirmed_amount_cents,0) FROM drafts WHERE id=$1 FOR UPDATE", draftID).Scan(&kind, &status, &raw, &confirmed); err != nil {
		return err
	}
	p, err := decodeMap(raw)
	if err != nil {
		return err
	}
	if chat < 0 {
		allowed, ok := telegramAllowedGroup()
		stored, _ := telegramInt(p["telegram_chat_id"])
		if !ok || allowed != chat || stored != chat {
			return errors.New("Рабочая группа изменилась")
		}
	} else {
		var linked bool
		if kind == "transfer" {
			saved, _ := telegramInt(p["telegram_recipient_id"])
			if saved != chat {
				return errors.New("Связь получателя изменилась")
			}
			err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND active AND role='collector' AND custodian_id::text=$2 AND telegram_id=$3)", str(p, "telegram_recipient_user_id"), str(p, "to_custodian_id"), chat).Scan(&linked)
		} else if kind == "repayment" {
			saved, _ := telegramInt(p["telegram_chat_id"])
			if saved != chat {
				return errors.New("Личный чат изменился")
			}
			err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM users u JOIN drafts d ON d.created_by=u.id WHERE d.id=$1 AND u.active AND u.telegram_id=$2 AND u.custodian_id::text=$3)", draftID, chat, str(p, "source_id")).Scan(&linked)
		}
		if err != nil || !linked {
			return errors.New("Связь личного чата изменилась")
		}
	}
	if status == "draft" && p["telegram_sender_confirmed"] != true {
		return nil
	}
	p["presentation_draft_id"] = draftID
	text, markup := telegramOperationSummary(kind, status, p, confirmed, chat > 0)
	if text == "" {
		return nil
	}
	if err = a.telegramPublishCard(tx, draftID, chat, text, markup); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) telegramSyncCards(limit int) error {
	for i := 0; i < limit; i++ {
		var draftID string
		var chat int64
		err := a.db.QueryRow(`UPDATE telegram_operation_messages m SET next_attempt_at=now()+interval '1 minute'
 WHERE (m.draft_id,m.chat_id)=(SELECT m.draft_id,m.chat_id FROM telegram_operation_messages m JOIN drafts d ON d.id=m.draft_id
 WHERE m.next_attempt_at<=now() AND (d.status<>'draft' OR d.payload->>'telegram_sender_confirmed'='true')
 AND m.rendered_state<>`+telegramRenderedStateSQL+` ORDER BY m.next_attempt_at,m.draft_id,m.chat_id LIMIT 1 FOR UPDATE OF m SKIP LOCKED)
 RETURNING draft_id,chat_id`).Scan(&draftID, &chat)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = a.telegramSyncCard(draftID, chat); err != nil {
			if _, saveErr := a.db.Exec("UPDATE telegram_operation_messages SET last_error=$3 WHERE draft_id=$1 AND chat_id=$2", draftID, chat, errTelegramDelivery.Error()); saveErr != nil {
				return saveErr
			}
		}
	}
	return nil
}

// Bind old cards only after the existing authorization checks succeed. Never
// let a stale duplicate callback replace the canonical preview's message ID.
func telegramBindCard(tx *sql.Tx, draftID string, chat, messageID int64) error {
	if messageID <= 0 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO telegram_operation_messages(draft_id,chat_id,message_id,rendered_state)
 VALUES($1,$2,$3,'legacy') ON CONFLICT DO NOTHING`, draftID, chat, messageID)
	return err
}

func (a *App) telegramResolveInputErrors(u telegramActor, chat int64, operation string) error {
	_, err := a.db.Exec("UPDATE telegram_input_errors SET resolved=true,next_attempt_at=now() WHERE user_id=$1 AND chat_id=$2 AND operation=$3 AND NOT resolved", u.ID, chat, operation)
	return err
}

func (a *App) telegramCleanupInputErrors(limit int) error {
	for i := 0; i < limit; i++ {
		var user string
		var chat, update int64
		var message sql.NullInt64
		err := a.db.QueryRow(`UPDATE telegram_input_errors SET next_attempt_at=now()+interval '1 minute'
 WHERE (user_id,chat_id,update_id)=(SELECT user_id,chat_id,update_id FROM telegram_input_errors WHERE resolved AND next_attempt_at<=now()
 ORDER BY next_attempt_at,update_id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING user_id,chat_id,update_id,message_id`).Scan(&user, &chat, &update, &message)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if message.Valid {
			err = a.telegramCall("deleteMessage", M{"chat_id": chat, "message_id": message.Int64})
		}
		var apiErr *telegramAPIError
		// A retry after successful deletion is also successful. Old messages beyond
		// Telegram's deletion window are retained visibly, with a diagnostic record.
		if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "message to delete not found") {
			err = nil
		}
		if err != nil {
			_, err = a.db.Exec("UPDATE telegram_input_errors SET last_error=$4 WHERE user_id=$1 AND chat_id=$2 AND update_id=$3", user, chat, update, errTelegramDelivery.Error())
		} else {
			_, err = a.db.Exec("DELETE FROM telegram_input_errors WHERE user_id=$1 AND chat_id=$2 AND update_id=$3 AND resolved", user, chat, update)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *App) telegramSendInputPrompt(u telegramActor, chat int64, command, text string, markup interface{}) error {
	var raw []byte
	if err := a.db.QueryRow("SELECT context FROM telegram_dialogs WHERE user_id=$1 AND command=$2 AND expires_at>now()", u.ID, command).Scan(&raw); err != nil {
		return err
	}
	context, err := decodeMap(raw)
	if err != nil {
		return err
	}
	messageID, err := a.telegramSendMessage(chat, text, markup)
	if err != nil {
		return err
	}
	context["prompt_message_id"], context["chat_id"] = messageID, chat
	result, err := a.db.Exec("UPDATE telegram_dialogs SET context=$3 WHERE user_id=$1 AND command=$2 AND context=$4::jsonb AND expires_at>now()", u.ID, command, encode(context), raw)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Ввод уже изменился. Повторите команду")
	}
	return nil
}
func (a *App) telegramPromptMessage(u telegramActor, chat int64, command string) int64 {
	var messageID int64
	_ = a.db.QueryRow("SELECT (context->>'prompt_message_id')::bigint FROM telegram_dialogs WHERE user_id=$1 AND command=$2 AND context->>'chat_id'=$3 AND expires_at>now()", u.ID, command, fmt.Sprint(chat)).Scan(&messageID)
	return messageID
}
func (a *App) telegramMessageIsOperationCard(chat, messageID int64) bool {
	var exists bool
	// On an unavailable database keep buttons: removing a live confirmation
	// keyboard would leave the author unable to finish the operation.
	if err := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM telegram_operation_messages WHERE chat_id=$1 AND message_id=$2)", chat, messageID).Scan(&exists); err != nil {
		return true
	}
	return exists
}

func (a *App) telegramRetryInputPrompt(u telegramActor, message *telegramMessage, updateID int64) (bool, error) {
	fields := strings.Fields(message.Text)
	if len(fields) != 1 || !strings.HasPrefix(fields[0], "/") {
		return false, nil
	}
	command := telegramMessageCommand(message.Text)
	switch command {
	case "withdraw", "expense", "handover", "transfer", "cashexpense":
	default:
		return false, nil
	}
	var prompt int64
	err := a.db.QueryRow("SELECT COALESCE((context->>'prompt_message_id')::bigint,0) FROM telegram_dialogs WHERE user_id=$1 AND command=$2 AND expires_at>now()", u.ID, command).Scan(&prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if prompt > 0 {
		return true, nil
	}
	return true, a.telegramHandleMessage(u, message, updateID)
}
