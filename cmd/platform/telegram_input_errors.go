package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Operation, field, code and row are independent of the localized text.
// Source messages are never deleted; only bot errors for the corrected flow.
type telegramInputError struct {
	Operation string
	Field     string
	Code      string
	Row       int
	Cause     error
}

func (e *telegramInputError) Error() string {
	if e.Row > 0 {
		return fmt.Sprintf("Строка %d: %s", e.Row, e.Cause.Error())
	}
	return e.Cause.Error()
}
func (e *telegramInputError) Unwrap() error { return e.Cause }
func telegramFieldError(operation, field, code, text string) error {
	return &telegramInputError{Operation: operation, Field: field, Code: code, Cause: errors.New(text)}
}
func telegramRowInputError(operation string, row int, cause error) error {
	var typed *telegramInputError
	if errors.As(cause, &typed) {
		copy := *typed
		copy.Row = row
		return &copy
	}
	return &telegramInputError{Operation: operation, Field: "input", Code: "invalid_input", Row: row, Cause: cause}
}

func (a *App) telegramInputOperation(u telegramActor, message *telegramMessage) string {
	command := telegramMessageCommand(message.Text)
	if command == "cancel" {
		var previous string
		if err := a.db.QueryRow("SELECT command FROM telegram_dialogs WHERE user_id=$1 AND expires_at>now()", u.ID).Scan(&previous); err == nil {
			return previous
		}
	}
	if strings.HasPrefix(strings.TrimSpace(message.Text), "/") || command == "зп" {
		if command == "зп" {
			return "salary"
		}
		if _, known := telegramOperationLabels[command]; known {
			return command
		}
		return "command"
	}
	var operation string
	if err := a.db.QueryRow("SELECT command FROM telegram_dialogs WHERE user_id=$1 AND expires_at>now()", u.ID).Scan(&operation); err == nil {
		return operation
	}
	// A webhook retry may arrive after the accepted input consumed its dialog.
	return "input"
}

var telegramOperationLabels = map[string]string{
	"withdraw": "Снятие", "expense": "Расход", "salary": "Зарплата", "handover": "Передача", "transfer": "Перевод",
	"card_status": "Статус карты", "block_card": "Блокировка карты", "repay": "Выдача мерчанту",
	"cards_balance": "Остатки карт", "my_balance": "Остаток наличных", "input": "Ввод",
}

func (a *App) telegramReportInputError(u telegramActor, chat, updateID int64, operation string, cause error) error {
	var typed *telegramInputError
	if !errors.As(cause, &typed) {
		typed = telegramClassifyInputError(operation, cause)
	}
	label := telegramOperationLabels[operation]
	if label == "" {
		label = "Команда"
	}
	text := "⚠️ " + label + ": " + typed.Error()
	// One delivery per incoming update. Lock the actor row so concurrent duplicate
	// webhooks cannot publish multiple copies of the same error.
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID string
	if err = tx.QueryRow("SELECT id FROM users WHERE id=$1 FOR UPDATE", u.ID).Scan(&userID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO telegram_input_errors(user_id,chat_id,update_id,operation,code,field,row_number,message)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, u.ID, chat, updateID, operation, typed.Code, typed.Field, typed.Row, text)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return a.telegramDeliverInputError(u.ID, chat, updateID)
}

func (a *App) telegramInputAccepted(u telegramActor, message *telegramMessage, updateID int64) bool {
	if telegramMessageCommand(message.Text) == "cancel" {
		return true
	}
	var accepted bool
	_ = a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM drafts WHERE created_by=$1 AND idempotency_key=$2)
 OR EXISTS(SELECT 1 FROM audit_events WHERE actor_id=$1 AND channel='telegram' AND action='card_status_command' AND outcome='success' AND detail->>'update_id'=$3)`, u.ID, fmt.Sprintf("telegram:%d", updateID), fmt.Sprint(updateID)).Scan(&accepted)
	return accepted
}
func (a *App) telegramAcceptedOperation(u telegramActor, updateID int64) string {
	var kind string
	var raw []byte
	if err := a.db.QueryRow("SELECT kind,payload FROM drafts WHERE created_by=$1 AND idempotency_key=$2", u.ID, fmt.Sprintf("telegram:%d", updateID)).Scan(&kind, &raw); err == nil {
		if kind == "withdrawal" {
			return "withdraw"
		}
		if kind == "expense" {
			p, _ := decodeMap(raw)
			items, _ := telegramExpenseItems(p)
			if len(items) == 1 && str(items[0], "category") == "salary" {
				return "salary"
			}
		}
		return kind
	}
	var command string
	_ = a.db.QueryRow("SELECT detail->>'command' FROM audit_events WHERE actor_id=$1 AND action='card_status_command' AND channel='telegram' AND detail->>'update_id'=$2", u.ID, fmt.Sprint(updateID)).Scan(&command)
	if command == "block_card" {
		return command
	}
	return "card_status"
}

// Classify legacy validators at the message boundary without changing their
// financial decisions. New parsers return field errors directly.
func telegramClassifyInputError(operation string, cause error) *telegramInputError {
	field, code := "input", "invalid_input"
	text := strings.ToLower(cause.Error())
	switch {
	case strings.Contains(text, "только тони") || strings.Contains(text, "только сборщику") || strings.Contains(text, "роль") || strings.Contains(text, "связь"):
		field, code = "actor", "permission_denied"
	case strings.Contains(text, "не удалось") || strings.Contains(text, "черновик не сохранён"):
		field, code = "system", "temporarily_unavailable"
	case strings.Contains(text, "истёк") || strings.Contains(text, "завершён") || strings.Contains(text, "устарел") || strings.Contains(text, "сначала") || strings.Contains(text, "начните"):
		field, code = "dialog", "invalid_state"
	case strings.Contains(text, "получател") || strings.Contains(text, "мерчант"):
		field, code = "recipient", "invalid_recipient"
	case strings.Contains(text, "статус"):
		field, code = "status", "invalid_status"
	case strings.Contains(text, "карт") || strings.Contains(text, "цифр"):
		field, code = "card", "invalid_card"
	case strings.Contains(text, "комментар"):
		field, code = "comment", "required_comment"
	case strings.Contains(text, "сумм") || strings.Contains(text, "формат"):
		field, code = "amount", "invalid_amount"
	}
	return &telegramInputError{Operation: operation, Field: field, Code: code, Cause: cause}
}

func (a *App) telegramDeliverInputError(user string, chat, updateID int64) error {
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var message string
	var messageID sql.NullInt64
	var resolved bool
	if err = tx.QueryRow("SELECT message,message_id,resolved FROM telegram_input_errors WHERE user_id=$1 AND chat_id=$2 AND update_id=$3 FOR UPDATE", user, chat, updateID).Scan(&message, &messageID, &resolved); err != nil {
		return err
	}
	if resolved || messageID.Valid {
		return nil
	}
	sent, err := a.telegramSendMessage(chat, message, nil)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE telegram_input_errors SET message_id=$4,last_error='' WHERE user_id=$1 AND chat_id=$2 AND update_id=$3", user, chat, updateID, sent); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) telegramDeliverInputErrors(limit int) error {
	for i := 0; i < limit; i++ {
		var user string
		var chat, update int64
		err := a.db.QueryRow(`UPDATE telegram_input_errors SET next_attempt_at=now()+interval '1 minute'
 WHERE (user_id,chat_id,update_id)=(SELECT user_id,chat_id,update_id FROM telegram_input_errors WHERE NOT resolved AND message_id IS NULL AND next_attempt_at<=now()
 ORDER BY next_attempt_at,update_id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING user_id,chat_id,update_id`).Scan(&user, &chat, &update)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = a.telegramDeliverInputError(user, chat, update); err != nil {
			if _, err = a.db.Exec("UPDATE telegram_input_errors SET last_error=$4 WHERE user_id=$1 AND chat_id=$2 AND update_id=$3", user, chat, update, errTelegramDelivery.Error()); err != nil {
				return err
			}
		}
	}
	return nil
}
