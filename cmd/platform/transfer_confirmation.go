package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func reservedCash(tx *sql.Tx, custodian string) (int64, error) {
	var cents int64
	err := tx.QueryRow("SELECT COALESCE(sum(amount_cents),0) FROM cash_reservations WHERE custodian_id=$1 AND released_at IS NULL", custodian).Scan(&cents)
	return cents, err
}
func availableCash(tx *sql.Tx, account, custodian string) (int64, error) {
	cents, err := balance(tx, account, "custodian", custodian)
	if err != nil || account != "1200" {
		return cents, err
	}
	reserved, err := reservedCash(tx, custodian)
	if err != nil {
		return 0, err
	}
	// Negative ledger balances are retained, but cannot be spent or reserved.
	if cents < 0 {
		return cents, nil
	}
	return cents - reserved, nil
}
func queueTransferNotification(tx *sql.Tx, draftID, purpose string) error {
	_, err := tx.Exec("INSERT INTO transfer_notifications(id,draft_id,purpose) VALUES($1,$2,$3) ON CONFLICT(draft_id,purpose) DO NOTHING", id(), draftID, purpose)
	return err
}

// Validate saved identities, never usernames or arbitrary chat IDs.
// Lock users only: the production role can update user binding columns,
// but custodians are read-only. Serializable reads still validate custodians.
func validateTransferParties(tx *sql.Tx, p M) error {
	for _, party := range []struct{ user, cust, telegram string }{
		{"telegram_sender_user_id", "from_custodian_id", "telegram_sender_id"},
		{"telegram_recipient_user_id", "to_custodian_id", "telegram_recipient_id"},
	} {
		var telegramID int64
		err := tx.QueryRow(`SELECT COALESCE(u.telegram_id,0) FROM users u JOIN custodians c ON c.id=u.custodian_id
   WHERE u.id=$1 AND u.custodian_id=$2 AND u.active AND u.role='collector' AND c.active AND c.kind='collector' FOR SHARE OF u`, str(p, party.user), str(p, party.cust)).Scan(&telegramID)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("Связь участника перевода изменилась; отмените черновик и создайте новый")
		}
		if err != nil {
			return err
		}
		saved, err := telegramInt(p[party.telegram])
		if err != nil || (saved > 0 && telegramID != saved) {
			return errors.New("Telegram участника перевода изменился; отмените черновик и создайте новый")
		}
	}
	return nil
}

func (a *App) telegramSenderTransfer(tx *sql.Tx, u telegramActor, draftID, status string, p M, action string) (string, bool) {
	if status != "draft" {
		return "Операция уже обработана", true
	}
	if p["telegram_preview_sent"] != true {
		return "Предпросмотр ещё не доставлен", false
	}
	if action == "reject" {
		if err := releaseTransfer(tx, draftID, u.ID, "telegram", "sender", "Отменено отправителем"); err != nil {
			return "Не удалось отменить перевод", false
		}
		if _, err := tx.Exec("UPDATE drafts SET status='rejected' WHERE id=$1", draftID); err != nil {
			return "Не удалось отменить перевод", false
		}
		if err := txAudit(tx, u.ID, "telegram", "draft_reject", "transfer", draftID, "success", "Отменено отправителем", M{}); err != nil {
			return "Не удалось записать аудит", false
		}
		if err := tx.Commit(); err != nil {
			return "Не удалось отменить перевод; повторите", false
		}
		return "Перевод отменён. Резерв освобождён", true
	}
	if p["telegram_sender_confirmed"] == true {
		return "Ожидается подтверждение получателя. Сумма зарезервирована", false
	}
	// Required also for legacy Telegram drafts upgraded by schema 023.
	p["telegram_sender_user_id"] = u.ID
	if err := validateTransferParties(tx, p); err != nil {
		return publicError(http.StatusConflict, err).message, false
	}
	if err := a.telegramValidateConfirmation(tx, u, "transfer", p); err != nil {
		return err.Error(), false
	}
	cents, err := telegramInt(p["amount_cents"])
	if err != nil || cents <= 0 {
		return "Сумма черновика некорректна", false
	}
	available, err := availableCash(tx, "1200", u.CustodianID)
	if err != nil {
		return "Не удалось проверить наличные", false
	}
	if available < cents {
		return "Недостаточно доступных наличных с учётом резервов", false
	}
	if _, err = tx.Exec("INSERT INTO cash_reservations(draft_id,custodian_id,amount_cents) VALUES($1,$2,$3)", draftID, u.CustodianID, cents); err != nil {
		return "Не удалось зарезервировать сумму", false
	}
	p["telegram_sender_confirmed"] = true
	p["telegram_recipient_confirmation_required"] = true
	p["telegram_sender_confirmed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec("UPDATE drafts SET payload=$1 WHERE id=$2", encode(p), draftID); err != nil {
		return "Не удалось сохранить подтверждение", false
	}
	for _, purpose := range []string{"recipient_request", "group_waiting"} {
		if err = queueTransferNotification(tx, draftID, purpose); err != nil {
			return "Не удалось сохранить уведомление", false
		}
	}
	if err = txAudit(tx, u.ID, "telegram", "transfer_sender_confirm", "transfer", draftID, "success", "", M{"reserved_cents": cents}); err != nil {
		return "Не удалось записать аудит", false
	}
	if err = tx.Commit(); err != nil {
		return "Не удалось подтвердить; повторите", false
	}
	return "Сумма зарезервирована. Ожидается подтверждение получателя", true
}

// Caller owns the draft row lock. Release and posting share one transaction.
func (a *App) postConfirmedTransfer(tx *sql.Tx, draftID string, p M, actor User, channel, acceptedBy string) error {
	if p["telegram_sender_confirmed"] != true {
		return errors.New("Сначала требуется подтверждение отправителя")
	}
	if err := validateTransferParties(tx, p); err != nil {
		return err
	}
	cents, err := telegramInt(p["amount_cents"])
	if err != nil || cents <= 0 {
		return errors.New("Сумма черновика некорректна")
	}
	var reserved int64
	var custodian string
	if err = tx.QueryRow("SELECT custodian_id,amount_cents FROM cash_reservations WHERE draft_id=$1 AND released_at IS NULL FOR UPDATE", draftID).Scan(&custodian, &reserved); err != nil || reserved != cents || custodian != str(p, "from_custodian_id") {
		return errors.New("Резерв перевода отсутствует или изменился")
	}
	if _, err = tx.Exec("UPDATE cash_reservations SET released_at=now() WHERE draft_id=$1", draftID); err != nil {
		return err
	}
	lines, err := a.eventLines(tx, "transfer", p, cents)
	if err != nil {
		return err
	}
	now := time.Now()
	if _, err = put(tx, "transfer", draftID, "draft:"+draftID, actor.ID, now, now, lines, ""); err != nil {
		return err
	}
	p["telegram_transfer_accepted_by"] = acceptedBy
	p["telegram_transfer_confirmed_by_name"] = actor.Name
	if acceptedBy == "recipient" {
		p["telegram_recipient_confirmed"] = true
	}
	p["telegram_transfer_confirmed_at"] = now.UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec("UPDATE drafts SET status='posted',payload=$1,confirmed_by=$2,confirmed_at=$3,confirmed_amount_cents=$4 WHERE id=$5", encode(p), actor.ID, now, cents, draftID); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE transfer_notifications SET state='cancelled' WHERE draft_id=$1 AND purpose IN ('recipient_request','group_waiting')", draftID); err != nil {
		return err
	}
	for _, purpose := range []string{"group_final", "recipient_final"} {
		if err = queueTransferNotification(tx, draftID, purpose); err != nil {
			return err
		}
	}
	return txAudit(tx, actor.ID, channel, "transfer_accept", "transfer", draftID, "success", "", M{"accepted_by": acceptedBy, "confirmed_cents": cents, "reservation_released": true})
}
func releaseTransfer(tx *sql.Tx, draftID, actor, channel, rejectedBy, reason string) error {
	var kind string
	var raw []byte
	if err := tx.QueryRow("SELECT kind,payload FROM drafts WHERE id=$1", draftID).Scan(&kind, &raw); err != nil {
		return err
	}
	p, err := decodeMap(raw)
	if err != nil {
		return err
	}
	if kind != "transfer" || p["telegram_recipient_confirmation_required"] != true {
		return nil
	}
	if _, err = tx.Exec("UPDATE cash_reservations SET released_at=now() WHERE draft_id=$1 AND released_at IS NULL", draftID); err != nil {
		return err
	}
	p["telegram_transfer_rejected_by"] = rejectedBy
	if _, err = tx.Exec("UPDATE drafts SET payload=$1 WHERE id=$2", encode(p), draftID); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE transfer_notifications SET state='cancelled' WHERE draft_id=$1 AND purpose IN ('recipient_request','group_waiting')", draftID); err != nil {
		return err
	}
	for _, purpose := range []string{"group_final", "recipient_final"} {
		if err = queueTransferNotification(tx, draftID, purpose); err != nil {
			return err
		}
	}
	return txAudit(tx, actor, channel, "transfer_reservation_release", "transfer", draftID, "success", reason, M{"rejected_by": rejectedBy})
}

func (a *App) telegramPrivateUpdate(w http.ResponseWriter, x telegramUpdate, sender, chat int64) {
	var u telegramActor
	err := a.db.QueryRow("SELECT id,login,name,role,COALESCE(custodian_id::text,'') FROM users WHERE telegram_id=$1 AND active AND role='collector'", sender).Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.CustodianID)
	if err != nil || u.CustodianID == "" {
		if x.CallbackQuery != nil {
			_ = a.telegramAnswer(x.CallbackQuery.ID, "Нет доступа к переводу")
		} else if x.Message != nil && telegramMessageCommand(x.Message.Text) == "start" {
			_ = a.telegramReply(chat, "Ваш Telegram ID не связан с активным сборщиком. Обратитесь к системному администратору.", nil)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if x.CallbackQuery != nil {
		message, remove := a.telegramRecipientTransfer(u, sender, chat, x.CallbackQuery.Data)
		if err = a.telegramAnswer(x.CallbackQuery.ID, message); err != nil {
			http.Error(w, "telegram delivery failed", 500)
			return
		}
		if remove {
			a.telegramRemoveButtons(chat, x.CallbackQuery.Message.MessageID)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if x.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	command := telegramMessageCommand(x.Message.Text)
	if command != "start" && command != "pending" {
		if strings.HasPrefix(strings.TrimSpace(x.Message.Text), "/") {
			_ = a.telegramReply(chat, "Здесь доступны /start и /pending — ожидающие переводы. Создавайте переводы в рабочей группе.", nil)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	tx, err := a.tx()
	if err != nil {
		http.Error(w, "database unavailable", 500)
		return
	}
	defer tx.Rollback()
	// Snapshot current binding in this transaction; /start never grants a role.
	var currentTelegram int64
	if err = tx.QueryRow("SELECT telegram_id FROM users WHERE id=$1 AND active AND role='collector' AND custodian_id=$2 FOR SHARE", u.ID, u.CustodianID).Scan(&currentTelegram); err != nil || currentTelegram != sender {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err = tx.Exec("INSERT INTO telegram_private_chats(user_id,telegram_id,chat_id) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET telegram_id=excluded.telegram_id,chat_id=excluded.chat_id,started_at=now()", u.ID, sender, chat); err != nil {
		http.Error(w, "database unavailable", 500)
		return
	}
	if _, err = tx.Exec(`UPDATE transfer_notifications n SET state='pending',next_attempt_at=now(),last_error='' FROM drafts d
  WHERE n.draft_id=d.id AND n.purpose='recipient_request' AND d.status='draft' AND d.payload->>'telegram_sender_confirmed'='true'
  AND d.payload->>'telegram_recipient_user_id'=$1`, u.ID); err != nil {
		http.Error(w, "database unavailable", 500)
		return
	}
	if err = txAudit(tx, u.ID, "telegram", "private_chat_start", "user", u.ID, "success", "", M{"update_id": x.UpdateID}); err != nil || tx.Commit() != nil {
		http.Error(w, "database unavailable", 500)
		return
	}
	if err = a.telegramReply(chat, "Личный чат подключён. Здесь будут запросы на подтверждение переводов. Ожидающие запросы отправлены в очередь; повторный просмотр — /pending.", nil); err != nil {
		http.Error(w, "telegram delivery failed", 500)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func (a *App) telegramRecipientTransfer(u telegramActor, sender, chat int64, data string) (string, bool) {
	action, draftID, ok := strings.Cut(data, ":")
	if !ok || (action != "receive" && action != "decline") || !validID(draftID) {
		return "Здесь можно только подтвердить или отклонить получение перевода", false
	}
	tx, err := a.tx()
	if err != nil {
		return "Не удалось открыть перевод; повторите", false
	}
	defer tx.Rollback()
	var status string
	var raw []byte
	if err = tx.QueryRow("SELECT payload,status FROM drafts WHERE id=$1 AND kind='transfer' FOR UPDATE", draftID).Scan(&raw, &status); err != nil {
		return "Перевод не найден", false
	}
	p, err := decodeMap(raw)
	if err != nil || p["telegram_recipient_confirmation_required"] != true || str(p, "telegram_recipient_user_id") != u.ID || str(p, "to_custodian_id") != u.CustodianID {
		return "Нет доступа к этому переводу", false
	}
	var registered bool
	if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM telegram_private_chats WHERE user_id=$1 AND telegram_id=$2 AND chat_id=$3)", u.ID, sender, chat).Scan(&registered); err != nil || !registered {
		return "Сначала отправьте /start", false
	}
	saved, _ := telegramInt(p["telegram_recipient_id"])
	if saved != sender {
		return "Telegram получателя изменился; требуется новый перевод", false
	}
	if status != "draft" {
		return "Перевод уже обработан", true
	}
	if p["telegram_sender_confirmed"] != true {
		return "Отправитель ещё не подтвердил перевод", false
	}
	if action == "decline" {
		err = releaseTransfer(tx, draftID, u.ID, "telegram", "recipient", "Отклонено получателем")
		if err == nil {
			_, err = tx.Exec("UPDATE drafts SET status='rejected' WHERE id=$1", draftID)
		}
	} else {
		err = a.postConfirmedTransfer(tx, draftID, p, u.User, "telegram", "recipient")
	}
	if err != nil {
		return publicError(http.StatusConflict, err).message, false
	}
	if err = tx.Commit(); err != nil {
		return "Не удалось сохранить; повторите", false
	}
	if action == "decline" {
		return "Перевод отклонён. Резерв освобождён", true
	}
	return "Получение подтверждено. Перевод проведён", true
}

func transferDescription(p M) string {
	cents, _ := telegramInt(p["amount_cents"])
	return fmt.Sprintf("Отправитель: %s\nПолучатель: %s\nСумма: %s", str(p, "telegram_actor_name"), str(p, "to_custodian_name"), telegramMoney(cents))
}
