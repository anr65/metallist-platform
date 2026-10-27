package main

import (
	"database/sql"
	"errors"
	"strings"
)

// Only call with an ID captured from the bot's own sendMessage response.
func queueTelegramPromptDeletion(tx *sql.Tx, chat, messageID int64) error {
	if messageID <= 0 {
		return nil
	}
	// An earlier release reused the hint as its financial confirmation. Never
	// delete a canonical operation card, including old previews and final states.
	_, err := tx.Exec(`INSERT INTO telegram_prompt_deletions(chat_id,message_id)
 SELECT $1,$2 WHERE NOT EXISTS(SELECT 1 FROM telegram_operation_messages WHERE chat_id=$1 AND message_id=$2)
 ON CONFLICT DO NOTHING`, chat, messageID)
	return err
}

func (a *App) telegramCleanupPrompts(limit int) error {
	for i := 0; i < limit; i++ {
		var chat, messageID int64
		err := a.db.QueryRow(`UPDATE telegram_prompt_deletions SET next_attempt_at=now()+interval '1 minute'
 WHERE (chat_id,message_id)=(SELECT chat_id,message_id FROM telegram_prompt_deletions WHERE next_attempt_at<=now()
 ORDER BY next_attempt_at,chat_id,message_id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING chat_id,message_id`).Scan(&chat, &messageID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// Recheck after claiming: a legacy callback may have bound this ID since
		// enqueueing. Cleanup cannot remove a confirmation or final receipt.
		var canonical bool
		if err = a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM telegram_operation_messages WHERE chat_id=$1 AND message_id=$2)", chat, messageID).Scan(&canonical); err != nil {
			return err
		}
		if !canonical {
			err = a.telegramCall("deleteMessage", M{"chat_id": chat, "message_id": messageID})
		}
		var apiErr *telegramAPIError
		if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "message to delete not found") {
			err = nil
		}
		if err != nil {
			_, err = a.db.Exec("UPDATE telegram_prompt_deletions SET last_error=$3 WHERE chat_id=$1 AND message_id=$2", chat, messageID, errTelegramDelivery.Error())
		} else {
			_, err = a.db.Exec("DELETE FROM telegram_prompt_deletions WHERE chat_id=$1 AND message_id=$2", chat, messageID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Capture the bot hint before consuming the dialog. Cancellation and durable
// cleanup must commit together, so a Telegram outage cannot orphan the picker.
func (a *App) telegramCancelDialog(userID string) (bool, error) {
	tx, err := a.tx()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var command string
	var raw []byte
	err = tx.QueryRow("DELETE FROM telegram_dialogs WHERE user_id=$1 RETURNING command,context", userID).Scan(&command, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if command == "cashexpense" {
		context, err := decodeMap(raw)
		if err != nil {
			return false, err
		}
		chat, _ := telegramInt(context["chat_id"])
		prompt, _ := telegramInt(context["prompt_message_id"])
		if chat != 0 {
			if err = queueTelegramPromptDeletion(tx, chat, prompt); err != nil {
				return false, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
