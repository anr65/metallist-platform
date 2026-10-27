package main

import (
	"fmt"
	"strings"
)

var telegramCashExpenseCategories = []string{"taxi", "delivery", "material", "communication", "other"}

func telegramCashExpenseCategory(category string) bool {
	for _, allowed := range telegramCashExpenseCategories {
		if category == allowed {
			return true
		}
	}
	return false
}

func (a *App) telegramCashExpenseStart(u telegramActor, chat int64) error {
	context := M{"token": id(), "chat_id": chat}
	_, err := a.db.Exec(`INSERT INTO telegram_dialogs(user_id,command,context,expires_at) VALUES($1,'cashexpense',$2,now()+interval '10 minutes')
 ON CONFLICT(user_id) DO UPDATE SET command=EXCLUDED.command,context=EXCLUDED.context,expires_at=EXCLUDED.expires_at,updated_at=now()`, u.ID, encode(context))
	if err != nil {
		return err
	}
	rows := []interface{}{}
	for _, category := range telegramCashExpenseCategories {
		rows = append(rows, []interface{}{M{"text": telegramCategoryName(category), "callback_data": "ce:" + str(context, "token") + ":" + category}})
	}
	return a.telegramSendInputPrompt(u, chat, "cashexpense", "Выберите тип расхода из наличных.\n/cancel — отмена", M{"inline_keyboard": rows})
}

func (a *App) telegramCashExpenseSelection(u telegramActor, chat, messageID int64, data string) (string, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || !telegramCashExpenseCategory(parts[2]) {
		return "Неизвестный тип расхода", false
	}
	tx, err := a.tx()
	if err != nil {
		return "Не удалось открыть ввод", false
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRow(`SELECT context FROM telegram_dialogs WHERE user_id=$1 AND command='cashexpense' AND expires_at>now() FOR UPDATE`, u.ID).Scan(&raw)
	if err != nil {
		return "Ввод завершён или истёк. Повторите /cashexpense", false
	}
	context, err := decodeMap(raw)
	if err != nil {
		return "Не удалось открыть ввод", false
	}
	storedChat, _ := telegramInt(context["chat_id"])
	prompt, _ := telegramInt(context["prompt_message_id"])
	if str(context, "token") != parts[1] || chat != storedChat || prompt != messageID {
		return "Только автор может выбрать тип расхода в своём активном вводе", false
	}
	if selected := str(context, "category"); selected != "" && selected != parts[2] {
		return "Тип уже выбран. Повторите /cashexpense для смены", false
	}
	context["category"] = parts[2]
	if _, err = tx.Exec("UPDATE telegram_dialogs SET context=$2,expires_at=now()+interval '10 minutes',updated_at=now() WHERE user_id=$1", u.ID, encode(context)); err != nil {
		return "Не удалось сохранить тип", false
	}
	text := "Расход из наличных: " + telegramCategoryName(parts[2]) + "\nВведите сумму, например: 1500 или 1,5к. Можно добавить комментарий.\n/cancel — отмена"
	if err = a.telegramEditMessageText(chat, messageID, text, telegramEmptyKeyboard()); err != nil {
		return errTelegramDelivery.Error(), false
	}
	if err = txAudit(tx, u.ID, "telegram", "cash_expense_category", "command", "", "success", "", M{"category": parts[2]}); err != nil {
		return "Не удалось сохранить выбор", false
	}
	if err = tx.Commit(); err != nil {
		return "Не удалось сохранить выбор", false
	}
	return "Введите сумму расхода", false
}

func (a *App) telegramCashExpenseInput(u telegramActor, message *telegramMessage, updateID int64, args string) error {
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRow(`SELECT context FROM telegram_dialogs WHERE user_id=$1 AND command='cashexpense' AND expires_at>now() FOR UPDATE`, u.ID).Scan(&raw); err != nil {
		return telegramFieldError("cashexpense", "category", "required_category", "Сначала выберите тип кнопкой /cashexpense")
	}
	context, err := decodeMap(raw)
	if err != nil {
		return err
	}
	chat, _ := telegramInt(context["chat_id"])
	category := str(context, "category")
	if chat != message.Chat.ID || !telegramCashExpenseCategory(category) {
		return telegramFieldError("cashexpense", "category", "required_category", "Сначала выберите тип расхода кнопкой")
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return telegramFieldError("cashexpense", "amount", "invalid_amount", "Введите положительную сумму расхода")
	}
	cents, err := telegramAmount(fields[0])
	if err != nil || cents <= 0 {
		return telegramFieldError("cashexpense", "amount", "invalid_amount", "Введите положительную сумму расхода, например: 1500 или 1,5к")
	}
	item := M{"source_kind": "cash", "source_id": u.CustodianID, "category": category, "amount_cents": cents, "comment": strings.TrimSpace(strings.TrimSpace(args)[len(fields[0]):])}
	p := M{"telegram_confirmation_required": true, "telegram_sender_confirmed": false, "telegram_preview_sent": false, "telegram_cash_expense": true, "telegram_chat_id": chat, "telegram_actor_name": u.Name, "telegram_prompt_message_id": context["prompt_message_id"], "amount_cents": cents, "amount": rub(cents), "expense_items": []M{item}}
	// Keep the ordinary expense payload usable by web history and reversal.
	for key, value := range item {
		if key != "amount_cents" {
			p[key] = value
		}
	}
	draft := id()
	if _, err = tx.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,'expense',$2,$3,$4)", draft, encode(p), u.ID, fmt.Sprintf("telegram:%d", updateID)); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1", u.ID); err != nil {
		return err
	}
	if err = txAudit(tx, u.ID, "telegram", "draft_create", "expense", draft, "success", "", M{"update_id": updateID, "category": category, "source_kind": "cash"}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return a.telegramSendPreview(draft, chat, p)
}
