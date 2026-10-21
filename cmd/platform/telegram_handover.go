package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Stable custodian identity documented in the owner's reconciliations.
// Display names and Telegram usernames must never grant financial permissions.
const telegramTonyCustodianID = "dd3c6b6c-2013-4d31-a92b-ab4654d2cb64"

func telegramValidateHandover(tx *sql.Tx, creator string, p M) error {
	if p["telegram_handover"] != true || str(p, "from_custodian_id") != telegramTonyCustodianID {
		return errors.New("Передача доступна только Тони Сопрано")
	}
	sender, err := telegramInt(p["telegram_sender_id"])
	if err != nil || sender <= 0 {
		return errors.New("Отправитель передачи изменился")
	}
	var userID string
	err = tx.QueryRow(`SELECT u.id FROM users u JOIN custodians c ON c.id=u.custodian_id
 WHERE u.id=$1 AND u.active AND u.role='collector' AND u.telegram_id=$2
 AND c.id=$3 AND c.active AND c.kind='collector' FOR SHARE OF u`, creator, sender, telegramTonyCustodianID).Scan(&userID)
	if err != nil {
		return errors.New("Передача доступна только активной учётной записи Тони Сопрано")
	}
	var recipient string
	if err = tx.QueryRow("SELECT id FROM custodians WHERE id=$1 AND active AND kind='chief'", str(p, "to_custodian_id")).Scan(&recipient); err != nil {
		return errors.New("Главный администратор недоступен")
	}
	return nil
}

func (a *App) telegramHandover(u telegramActor, message *telegramMessage, updateID int64, args string) error {
	if u.Role != "collector" || u.CustodianID != telegramTonyCustodianID {
		return errors.New("Передача доступна только Тони Сопрано")
	}
	if args == "" {
		if _, err := a.db.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1", u.ID); err != nil {
			return errors.New("Не удалось завершить предыдущий ввод")
		}
		return a.telegramReply(message.Chat.ID, "Отправьте команду с суммой: /handover 100000 или /handover 100к. После вашего подтверждения получение подтверждает главный администратор в кабинете.", nil)
	}
	if len(strings.Fields(args)) != 1 {
		return errors.New("Формат: /handover 100000 — только сумма, получатель выбран автоматически")
	}
	cents, err := telegramAmount(args)
	if err != nil || cents <= 0 {
		return errors.New("Укажите положительную сумму передачи: /handover 100000")
	}
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var recipient, name string
	if err = tx.QueryRow("SELECT id,name FROM custodians WHERE kind='chief' AND active").Scan(&recipient, &name); err != nil {
		return errors.New("Главный администратор недоступен")
	}
	p := M{"telegram_handover": true, "telegram_confirmation_required": true, "telegram_sender_confirmed": false,
		"telegram_preview_sent": false, "telegram_chat_id": message.Chat.ID, "telegram_sender_id": message.From.ID,
		"telegram_actor_name": u.Name, "amount": rub(cents), "amount_cents": cents,
		"from_custodian_id": u.CustodianID, "to_custodian_id": recipient, "to_custodian_name": name}
	if err = telegramValidateHandover(tx, u.ID, p); err != nil {
		return err
	}
	draftID := id()
	if _, err = tx.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,'handover',$2,$3,$4)", draftID, encode(p), u.ID, fmt.Sprintf("telegram:%d", updateID)); err != nil {
		return errors.New("Черновик не сохранён; повторите ввод")
	}
	if _, err = tx.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1", u.ID); err != nil {
		return err
	}
	if err = txAudit(tx, u.ID, "telegram", "draft_create", "handover", draftID, "success", "", M{"update_id": updateID}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return a.telegramSendPreview(draftID, message.Chat.ID, p)
}

func (a *App) telegramConfirmHandover(tx *sql.Tx, u telegramActor, draftID string, p M, action string, chat int64) (string, bool) {
	if action == "reject" {
		if _, err := tx.Exec("UPDATE drafts SET status='rejected',payload=jsonb_set(payload,'{telegram_sender_confirmed}','false'::jsonb) WHERE id=$1", draftID); err != nil {
			return "Не удалось отклонить", false
		}
		if err := txAudit(tx, u.ID, "telegram", "draft_reject", "handover", draftID, "success", "", M{}); err != nil || tx.Commit() != nil {
			return "Не удалось отклонить", false
		}
		_ = a.telegramReply(chat, "Передача отклонена. Балансы не изменились.", nil)
		return "Отклонено", true
	}
	if err := telegramValidateHandover(tx, u.ID, p); err != nil {
		a.logAudit(u.ID, "telegram", "handover_sender_confirm", "handover", draftID, "rejected", err.Error(), M{})
		return err.Error(), false
	}
	if p["telegram_sender_confirmed"] == true {
		return "Заявка уже ожидает подтверждения главного администратора", true
	}
	if _, err := tx.Exec("UPDATE drafts SET payload=jsonb_set(payload,'{telegram_sender_confirmed}','true'::jsonb) WHERE id=$1", draftID); err != nil {
		return "Не удалось подтвердить заявку", false
	}
	if err := txAudit(tx, u.ID, "telegram", "handover_sender_confirm", "handover", draftID, "success", "", M{}); err != nil || tx.Commit() != nil {
		return "Не удалось подтвердить заявку", false
	}
	_ = a.telegramReply(chat, "Заявка на передачу "+str(p, "amount")+" ₽ ожидает личного подтверждения главного администратора в кабинете. До подтверждения наличные остаются за вами.", nil)
	return "Заявка подтверждена", true
}
