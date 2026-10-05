package main

import (
	"database/sql"
	"errors"
	"log"
	"time"
)

func (a *App) telegramTransferNotificationWorker() {
	for {
		if err := a.deliverTransferNotifications(10); err != nil {
			log.Printf("Telegram transfer notifications: %v", err)
		}
		time.Sleep(3 * time.Second)
	}
}

// Claim with a renewable retry deadline. A crash before acknowledgement may
// repeat a message, but can never repeat a financial posting.
func (a *App) deliverTransferNotifications(limit int) error {
	for i := 0; i < limit; i++ {
		var notificationID, draftID, purpose string
		err := a.db.QueryRow(`UPDATE transfer_notifications SET next_attempt_at=now()+interval '1 minute',attempts=attempts+1
   WHERE id=(SELECT id FROM transfer_notifications WHERE state='pending' AND next_attempt_at<=now()
   ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id,draft_id,purpose`).Scan(&notificationID, &draftID, &purpose)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = a.deliverTransferNotification(notificationID, draftID, purpose); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) deliverTransferNotification(notificationID, draftID, purpose string) error {
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, state string
	var raw []byte
	if err = tx.QueryRow("SELECT payload,status FROM drafts WHERE id=$1 FOR UPDATE", draftID).Scan(&raw, &status); err != nil {
		return err
	}
	if err = tx.QueryRow("SELECT state FROM transfer_notifications WHERE id=$1 FOR UPDATE", notificationID).Scan(&state); err != nil {
		return err
	}
	if state != "pending" {
		return nil
	}
	p, err := decodeMap(raw)
	if err != nil {
		return err
	}
	waiting := purpose == "recipient_request" || purpose == "group_waiting"
	if (waiting && (status != "draft" || p["telegram_sender_confirmed"] != true)) || (!waiting && status != "posted" && status != "rejected") {
		_, err = tx.Exec("UPDATE transfer_notifications SET state='cancelled' WHERE id=$1", notificationID)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	var chat int64
	var markup interface{}
	text := transferDescription(p)
	deliveryErr := error(nil)
	if purpose == "recipient_request" || purpose == "recipient_final" {
		var currentTelegram int64
		err = tx.QueryRow(`SELECT pc.chat_id,u.telegram_id FROM users u JOIN telegram_private_chats pc ON pc.user_id=u.id AND pc.telegram_id=u.telegram_id
   JOIN custodians c ON c.id=u.custodian_id WHERE u.id=$1 AND u.active AND u.role='collector' AND u.custodian_id=$2 AND c.active AND c.kind='collector'`, str(p, "telegram_recipient_user_id"), str(p, "to_custodian_id")).Scan(&chat, &currentTelegram)
		if err != nil {
			deliveryErr = errors.New("Получатель ещё не подключил личный чат или его связь изменилась")
		} else {
			saved, _ := telegramInt(p["telegram_recipient_id"])
			if saved > 0 && saved != currentTelegram {
				deliveryErr = errors.New("Telegram получателя изменился; требуется новый перевод")
			}
			if deliveryErr == nil && saved == 0 {
				p["telegram_recipient_id"] = currentTelegram
				if _, err = tx.Exec("UPDATE drafts SET payload=$1 WHERE id=$2", encode(p), draftID); err != nil {
					return err
				}
			}
		}
	} else {
		chat, err = telegramInt(p["telegram_chat_id"])
		allowed, ok := telegramAllowedGroup()
		if err != nil || !ok || chat != allowed {
			deliveryErr = errors.New("Исходная рабочая группа больше не подключена")
		}
	}
	switch purpose {
	case "recipient_request":
		text = "Подтвердите фактическое получение наличных\n\n" + text + "\n\nДо подтверждения перевод остаётся черновиком. Подтвердить можно только всю сумму."
		markup = M{"inline_keyboard": []interface{}{[]interface{}{M{"text": "Подтвердить получение", "callback_data": "receive:" + draftID}, M{"text": "Отклонить", "callback_data": "decline:" + draftID}}}}
	case "group_waiting":
		text = "Перевод ожидает подтверждения получателя\n\n" + text + "\n\nСумма зарезервирована у отправителя. Получатель должен открыть личный чат с ботом и нажать /start. Главный администратор также может подтвердить перевод в кабинете."
		markup = M{"inline_keyboard": []interface{}{[]interface{}{M{"text": "Отменить перевод", "callback_data": "reject:" + draftID}}}}
	default:
		if status == "posted" {
			who := "получателем"
			if str(p, "telegram_transfer_accepted_by") == "chief" {
				who = "главным администратором: " + str(p, "telegram_transfer_confirmed_by_name")
			}
			text = "Перевод проведён. Подтверждён " + who + ". Остатки обновлены.\n\n" + text
		} else {
			text = "Перевод отменён. Резерв освобождён, остатки не изменились.\n\n" + text
		}
	}
	if deliveryErr == nil {
		deliveryErr = a.telegramReply(chat, text, markup)
	}
	if deliveryErr != nil {
		if _, err = tx.Exec("UPDATE transfer_notifications SET last_error=$1,next_attempt_at=now()+interval '1 minute' WHERE id=$2", deliveryErr.Error(), notificationID); err != nil {
			return err
		}
		// State and next retry survive restarts; no sensitive Telegram URL is logged.
		return tx.Commit()
	}
	if _, err = tx.Exec("UPDATE transfer_notifications SET state='sent',last_error='' WHERE id=$1", notificationID); err != nil {
		return err
	}
	if err = txAudit(tx, "", "telegram", "transfer_notification", "transfer", draftID, "success", "", M{"purpose": purpose}); err != nil {
		return err
	}
	return tx.Commit()
}
