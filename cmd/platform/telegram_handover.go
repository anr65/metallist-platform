package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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
		if _, err := a.db.Exec("INSERT INTO telegram_dialogs(user_id,command,context,expires_at) VALUES($1,'handover',$2,now()+interval '10 minutes') ON CONFLICT(user_id) DO UPDATE SET command=EXCLUDED.command,context=EXCLUDED.context,expires_at=EXCLUDED.expires_at,updated_at=now()", u.ID, encode(M{"chat_id": message.Chat.ID})); err != nil {
			return errors.New("Не удалось завершить предыдущий ввод")
		}
		return a.telegramSendInputPrompt(u, message.Chat.ID, "handover", "Введите сумму передачи отдельным сообщением: 100000 или 100к. После вашего подтверждения вся сумма сразу поступит главному администратору. /cancel — отмена.", nil)
	}
	if len(strings.Fields(args)) != 1 {
		return telegramFieldError("handover", "format", "invalid_format", "Введите только сумму: 100000 или 100к. Получатель выбран автоматически")
	}
	cents, err := telegramAmount(args)
	if err != nil || cents <= 0 {
		return telegramFieldError("handover", "amount", "invalid_amount", "Укажите положительную сумму передачи: 100000 или 100к")
	}
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !strings.HasPrefix(strings.TrimSpace(message.Text), "/") {
		var storedChat int64
		if err = tx.QueryRow("SELECT (context->>'chat_id')::bigint FROM telegram_dialogs WHERE user_id=$1 AND command='handover' AND expires_at>now() FOR UPDATE", u.ID).Scan(&storedChat); err != nil || storedChat != message.Chat.ID {
			return errors.New("Ввод передачи завершён. Повторите /handover")
		}
	}
	var recipient, name string
	if err = tx.QueryRow("SELECT id,name FROM custodians WHERE kind='chief' AND active").Scan(&recipient, &name); err != nil {
		return errors.New("Главный администратор недоступен")
	}
	p := M{"telegram_prompt_message_id": a.telegramPromptMessage(u, message.Chat.ID, "handover"), "telegram_handover": true, "telegram_handover_single_confirmation": true, "telegram_confirmation_required": true, "telegram_sender_confirmed": false,
		"telegram_preview_sent": false, "telegram_chat_id": message.Chat.ID, "telegram_sender_id": message.From.ID,
		"telegram_actor_name": u.Name, "telegram_raw": message.Text, "amount": rub(cents), "amount_cents": cents,
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
		return "Отклонено", true
	}
	if err := telegramValidateHandover(tx, u.ID, p); err != nil {
		a.logAudit(u.ID, "telegram", "handover_sender_confirm", "handover", draftID, "rejected", err.Error(), M{})
		return err.Error(), false
	}
	if p["telegram_handover_single_confirmation"] != true {
		return "Условия передачи изменились. Отклоните старую заявку и повторите /handover для подтверждения немедленной передачи", false
	}
	cents, err := telegramInt(p["amount_cents"])
	if err != nil || cents <= 0 {
		return "Некорректная сумма передачи", false
	}
	lines, err := a.eventLines(tx, "handover", p, cents)
	if err != nil {
		return "Передача не проведена: " + err.Error(), false
	}
	now := time.Now()
	if _, err = put(tx, "handover", draftID, "draft:"+draftID, u.ID, now, now, lines, ""); err != nil {
		return "Не удалось провести передачу", false
	}
	if err := telegramCaptureBalance(tx, "handover", p, u.ID); err != nil {
		return "Не удалось сохранить баланс", false
	}
	p["telegram_sender_confirmed"] = true
	if _, err = tx.Exec("UPDATE drafts SET status='posted',payload=$1,confirmed_by=$2,confirmed_at=$3,confirmed_amount_cents=$4 WHERE id=$5", encode(p), u.ID, now, cents, draftID); err != nil {
		return "Не удалось сохранить передачу", false
	}
	if err = txAudit(tx, u.ID, "telegram", "draft_confirm", "handover", draftID, "success", "", M{"confirmed_cents": cents, "single_confirmation": true}); err != nil {
		return "Не удалось записать аудит", false
	}
	if err = tx.Commit(); err != nil {
		return "Не удалось провести передачу; повторите", false
	}
	return "Передача проведена", true
}
