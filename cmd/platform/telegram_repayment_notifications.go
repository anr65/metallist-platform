package main

import (
	"database/sql"
	"errors"
)

// The outbox is committed together with the posting. Telegram does not provide
// sendMessage idempotency: a crash after delivery may repeat a notification,
// but retrying delivery never runs a financial command.
func (a *App) deliverRepaymentNotifications(limit int) error {
	for i := 0; i < limit; i++ {
		var draftID, message string
		var chat int64
		err := a.db.QueryRow(`UPDATE telegram_repayment_notifications SET
 next_attempt_at=now()+interval '1 minute',attempts=attempts+1
 WHERE draft_id=(SELECT draft_id FROM telegram_repayment_notifications
 WHERE state='pending' AND next_attempt_at<=now() ORDER BY next_attempt_at,draft_id
 LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING draft_id,chat_id,message`).Scan(&draftID, &chat, &message)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		group, configured := telegramAllowedGroup()
		deliveryErr := error(nil)
		if !configured || chat != group {
			deliveryErr = errors.New("Группа уведомлений больше не подключена")
		}
		var status string
		if err = a.db.QueryRow("SELECT status FROM drafts WHERE id=$1 AND kind='repayment'", draftID).Scan(&status); err != nil {
			return err
		}
		if status == "reversed" {
			message += "\nЭта операция уже сторнирована."
		} else if status != "posted" {
			deliveryErr = errors.New("Возврат не проведён")
		}
		if deliveryErr == nil {
			deliveryErr = a.telegramReply(chat, message, nil)
		}
		if deliveryErr != nil {
			if _, err = a.db.Exec("UPDATE telegram_repayment_notifications SET last_error=$1 WHERE draft_id=$2 AND state='pending'", deliveryErr.Error(), draftID); err != nil {
				return err
			}
			continue
		}
		tx, err := a.tx()
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE telegram_repayment_notifications SET state='sent',last_error='' WHERE draft_id=$1 AND state='pending'", draftID)
		if err == nil {
			err = txAudit(tx, "", "telegram", "repayment_notification", "repayment", draftID, "success", "", M{})
		}
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return err
		}
	}
	return nil
}
