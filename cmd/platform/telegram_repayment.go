package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const telegramRepaymentPageSize = 10

type telegramRepaymentMerchant struct {
	ID   string
	Name string
}

type telegramRepaymentDialog struct {
	PromptMessageID int64
	Token           string
	Sender          int64
	Custodian       string
	Page            int
	HasNext         bool
	Merchants       []telegramRepaymentMerchant
	Selected        telegramRepaymentMerchant
	DraftID         string
	LastUpdate      int64
}

func telegramValidateRepaymentActor(tx *sql.Tx, u telegramActor, sender, chat int64) error {
	if sender <= 0 || chat != sender || u.Role != "collector" || u.CustodianID != telegramTonyCustodianID {
		return errors.New("Возврат доступен только Тони Сопрано в личном чате")
	}
	var currentTelegram int64
	err := tx.QueryRow(`SELECT u.telegram_id FROM users u JOIN custodians c ON c.id=u.custodian_id
 WHERE u.id=$1 AND u.active AND u.role='collector' AND c.id=$2 AND c.active AND c.kind='collector'
 FOR SHARE OF u`, u.ID, telegramTonyCustodianID).Scan(&currentTelegram)
	if err != nil || currentTelegram != sender {
		return errors.New("Связь Тони с Telegram или хранителем изменилась. Начните заново")
	}
	return nil
}

func loadRepaymentDialog(tx *sql.Tx, userID string) (telegramRepaymentDialog, error) {
	var d telegramRepaymentDialog
	var raw []byte
	err := tx.QueryRow("SELECT context,last_update_id FROM telegram_repayment_dialogs WHERE user_id=$1 AND expires_at>now() FOR UPDATE", userID).Scan(&raw, &d.LastUpdate)
	if err != nil {
		return d, err
	}
	lastUpdate := d.LastUpdate
	err = json.Unmarshal(raw, &d)
	d.LastUpdate = lastUpdate
	return d, err
}

func saveRepaymentDialog(tx *sql.Tx, userID string, d telegramRepaymentDialog) error {
	_, err := tx.Exec(`INSERT INTO telegram_repayment_dialogs(user_id,context,last_update_id,expires_at)
 VALUES($1,$2,$3,now()+interval '10 minutes') ON CONFLICT(user_id) DO UPDATE SET
 context=excluded.context,last_update_id=excluded.last_update_id,expires_at=excluded.expires_at`, userID, encode(d), d.LastUpdate)
	return err
}

func repaymentDialogMatches(d telegramRepaymentDialog, u telegramActor, sender int64) bool {
	return d.Sender == sender && d.Custodian == u.CustodianID && d.Token != ""
}

// Only names are selected for presentation. Debt, rates and other merchant
// details never enter the private dialog or Telegram response.
func repaymentMerchantPage(tx *sql.Tx, d *telegramRepaymentDialog, page int) error {
	if page < 0 || page > 100000 {
		return errors.New("Страница недоступна")
	}
	rows, err := tx.Query("SELECT id,name FROM merchants WHERE active ORDER BY name,id LIMIT $1 OFFSET $2", telegramRepaymentPageSize+1, page*telegramRepaymentPageSize)
	if err != nil {
		return err
	}
	defer rows.Close()
	merchants := []telegramRepaymentMerchant{}
	for rows.Next() {
		var merchant telegramRepaymentMerchant
		if err = rows.Scan(&merchant.ID, &merchant.Name); err != nil {
			return err
		}
		merchants = append(merchants, merchant)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(merchants) == 0 && page > 0 {
		return errors.New("Страница недоступна. Начните заново: /repay")
	}
	d.HasNext = len(merchants) > telegramRepaymentPageSize
	if d.HasNext {
		merchants = merchants[:telegramRepaymentPageSize]
	}
	d.Token, d.Page, d.Merchants = id(), page, merchants
	d.Selected = telegramRepaymentMerchant{}
	d.DraftID = ""
	return nil
}

func (a *App) telegramRepaymentPrompt(u telegramActor, chat, messageID int64, d telegramRepaymentDialog) error {
	text := "Выберите мерчанта"
	var markup interface{}
	if d.DraftID != "" {
		return a.telegramRepaymentPreview(d.DraftID, chat)
	}
	if d.Selected.ID != "" {
		text = "Мерчант: " + d.Selected.Name + "\n\nВведите сумму выдачи, например 100000 или 100к.\nДля отмены: /cancel"
	} else if len(d.Merchants) == 0 {
		text = "Нет доступных мерчантов. Для отмены: /cancel"
	} else {
		keyboard := []interface{}{}
		for i, merchant := range d.Merchants {
			keyboard = append(keyboard, []interface{}{M{"text": merchant.Name, "callback_data": fmt.Sprintf("rm:%s:%d", d.Token, i)}})
		}
		navigation := []interface{}{}
		if d.Page > 0 {
			navigation = append(navigation, M{"text": "Назад", "callback_data": fmt.Sprintf("rp:%s:%d", d.Token, d.Page-1)})
		}
		if d.HasNext {
			navigation = append(navigation, M{"text": "Далее", "callback_data": fmt.Sprintf("rp:%s:%d", d.Token, d.Page+1)})
		}
		if len(navigation) > 0 {
			keyboard = append(keyboard, navigation)
		}
		text += fmt.Sprintf(" · страница %d\n\nДля отмены: /cancel", d.Page+1)
		markup = M{"inline_keyboard": keyboard}
	}
	if messageID != 0 {
		// Telegram retains an old keyboard unless an empty replacement is sent.
		if markup == nil {
			markup = M{"inline_keyboard": []interface{}{}}
		}
		return a.telegramEditMessageText(chat, messageID, text, markup)
	}
	if d.PromptMessageID > 0 {
		return a.telegramEditMessageText(chat, d.PromptMessageID, text, markup)
	}
	sent, err := a.telegramSendMessage(chat, text, markup)
	if err != nil {
		return err
	}
	result, err := a.db.Exec("UPDATE telegram_repayment_dialogs SET context=jsonb_set(context,'{PromptMessageID}',to_jsonb($2::bigint)) WHERE user_id=$1 AND context->>'Token'=$3 AND expires_at>now()", u.ID, sent, d.Token)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Ввод уже изменился. Повторите /repay")
	}
	return nil
}

func rejectPreviousRepayment(tx *sql.Tx, u telegramActor) error {
	// Include expired dialogs so starting again cannot leave live stale buttons.
	var draftID string
	err := tx.QueryRow("SELECT COALESCE(context->>'DraftID','') FROM telegram_repayment_dialogs WHERE user_id=$1 FOR UPDATE", u.ID).Scan(&draftID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if draftID == "" {
		return nil
	}
	res, err := tx.Exec("UPDATE drafts SET status='rejected' WHERE id=$1 AND created_by=$2 AND kind='repayment' AND status='draft' AND payload->>'telegram_private_repayment'='true'", draftID, u.ID)
	if err != nil {
		return err
	}
	if changed, _ := res.RowsAffected(); changed > 0 {
		return txAudit(tx, u.ID, "telegram", "draft_reject", "repayment", draftID, "success", "Отмена или новый ввод возврата", M{})
	}
	return nil
}

func (a *App) telegramRepaymentMessage(u telegramActor, message *telegramMessage, updateID int64) error {
	chat, sender := message.Chat.ID, message.From.ID
	tx, err := a.tx()
	if err != nil {
		return errors.New("Не удалось открыть возврат. Повторите команду")
	}
	defer tx.Rollback()
	if err = telegramValidateRepaymentActor(tx, u, sender, chat); err != nil {
		return err
	}
	group, configured := telegramAllowedGroup()
	if !configured {
		return errors.New("Группа для уведомления о выдаче не настроена")
	}
	res, err := tx.Exec("INSERT INTO telegram_updates(update_id,raw_update) VALUES($1,$2) ON CONFLICT DO NOTHING", updateID, encode(M{"update_id": updateID, "from_id": sender, "chat_id": chat, "type": "private_repayment"}))
	if err != nil {
		return errors.New("Не удалось сохранить ввод. Повторите команду")
	}
	if inserted, _ := res.RowsAffected(); inserted == 0 {
		var draftID, status string
		err = tx.QueryRow("SELECT id,status FROM drafts WHERE idempotency_key=$1 AND kind='repayment' AND created_by=$2", fmt.Sprintf("telegram:%d", updateID), u.ID).Scan(&draftID, &status)
		if err == nil {
			tx.Rollback()
			if status == "draft" {
				return a.telegramRepaymentPreview(draftID, chat)
			}
			return nil
		}
		d, loadErr := loadRepaymentDialog(tx, u.ID)
		tx.Rollback()
		if loadErr == nil && d.LastUpdate == updateID && repaymentDialogMatches(d, u, sender) {
			return a.telegramRepaymentPrompt(u, chat, 0, d)
		}
		return nil
	}
	command := telegramMessageCommand(message.Text)
	if command == "repay" || command == "cancel" {
		if err = rejectPreviousRepayment(tx, u); err != nil {
			return errors.New("Не удалось завершить предыдущий ввод")
		}
		if command == "cancel" {
			if _, err = tx.Exec("DELETE FROM telegram_repayment_dialogs WHERE user_id=$1", u.ID); err != nil {
				return errors.New("Не удалось отменить ввод")
			}
			if err = txAudit(tx, u.ID, "telegram", "repayment_input_cancel", "user", u.ID, "success", "", M{"update_id": updateID}); err != nil || tx.Commit() != nil {
				return errors.New("Не удалось отменить ввод")
			}
			return a.telegramReply(chat, "Возврат отменён. Нового списания нет.", nil)
		}
		d := telegramRepaymentDialog{Sender: sender, Custodian: u.CustodianID, LastUpdate: updateID}
		if err = repaymentMerchantPage(tx, &d, 0); err != nil {
			return errors.New("Не удалось открыть список мерчантов")
		}
		if err = saveRepaymentDialog(tx, u.ID, d); err != nil || tx.Commit() != nil {
			return errors.New("Не удалось начать ввод")
		}
		return a.telegramRepaymentPrompt(u, chat, 0, d)
	}
	d, err := loadRepaymentDialog(tx, u.ID)
	if err != nil || !repaymentDialogMatches(d, u, sender) || d.Selected.ID == "" {
		return errors.New("Начните возврат командой /repay и выберите мерчанта")
	}
	if d.DraftID != "" {
		return errors.New("Сумма уже введена. Подтвердите или отмените возврат кнопкой; для нового ввода: /repay")
	}
	cents, err := telegramAmount(strings.TrimSpace(message.Text))
	if err != nil || cents <= 0 {
		return telegramFieldError("repay", "amount", "invalid_amount", "Укажите положительную сумму, например 100000 или 100к")
	}
	var name string
	if err = tx.QueryRow("SELECT name FROM merchants WHERE id=$1 AND active", d.Selected.ID).Scan(&name); err != nil || name != d.Selected.Name {
		return errors.New("Мерчант недоступен или его название изменилось. Начните заново: /repay")
	}
	draftID := id()
	p := M{"telegram_prompt_message_id": d.PromptMessageID, "telegram_private_repayment": true, "telegram_confirmation_required": true, "telegram_sender_confirmed": false,
		"telegram_preview_sent": false, "telegram_chat_id": chat, "telegram_sender_id": sender, "telegram_actor_name": u.Name,
		"telegram_notification_chat_id": group, "source_kind": "cash", "source_id": u.CustodianID,
		"merchant_id": d.Selected.ID, "merchant_name": name, "amount_cents": cents, "amount": rub(cents), "currency": "RUB"}
	if _, err = tx.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,'repayment',$2,$3,$4)", draftID, encode(p), u.ID, fmt.Sprintf("telegram:%d", updateID)); err != nil {
		return errors.New("Не удалось сохранить возврат")
	}
	d.DraftID, d.LastUpdate = draftID, updateID
	if err = saveRepaymentDialog(tx, u.ID, d); err != nil {
		return errors.New("Не удалось сохранить возврат")
	}
	if err = txAudit(tx, u.ID, "telegram", "draft_create", "repayment", draftID, "success", "", M{"update_id": updateID}); err != nil || tx.Commit() != nil {
		return errors.New("Не удалось сохранить возврат")
	}
	return a.telegramRepaymentPreview(draftID, chat)
}

func (a *App) telegramRepaymentPreview(draftID string, chat int64) error {
	var raw []byte
	var status string
	if err := a.db.QueryRow("SELECT payload,status FROM drafts WHERE id=$1 AND kind='repayment'", draftID).Scan(&raw, &status); err != nil {
		return errors.New("Возврат недоступен")
	}
	p, err := decodeMap(raw)
	storedChat, _ := telegramInt(p["telegram_chat_id"])
	if err != nil || storedChat != chat || p["telegram_private_repayment"] != true || status != "draft" {
		return nil
	}
	cents, _ := telegramInt(p["amount_cents"])
	text := "Подтвердите выдачу мерчанту\n\nМерчант: " + str(p, "merchant_name") + "\nСумма: " + telegramMoney(cents) + "\n\nДеньги будут списаны с ваших наличных. Подтверждайте только фактически выданную сумму."
	keyboard := M{"inline_keyboard": []interface{}{[]interface{}{M{"text": "Подтвердить выдачу", "callback_data": "rc:" + draftID}, M{"text": "Отменить", "callback_data": "rr:" + draftID}}}}
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("SELECT status FROM drafts WHERE id=$1 FOR UPDATE", draftID).Scan(&status); err != nil {
		return err
	}
	if status != "draft" {
		return nil
	}
	if err = a.telegramPublishCard(tx, draftID, chat, text, keyboard); err != nil {
		return err
	}
	prompt, _ := telegramInt(p["telegram_prompt_message_id"])
	if err = queueTelegramPromptDeletion(tx, chat, prompt); err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE drafts SET payload=jsonb_set(payload,'{telegram_preview_sent}','true'::jsonb) WHERE id=$1 AND status='draft'", draftID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_ = a.telegramCleanupPrompts(10)
	return nil
}

func (a *App) telegramRepaymentResult(u telegramActor, chat, messageID int64, data string) error {
	_, draftID, _ := strings.Cut(data, ":")
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRow("SELECT payload FROM drafts WHERE id=$1 AND kind='repayment' AND created_by=$2 FOR UPDATE", draftID, u.ID).Scan(&raw); err != nil {
		return errTelegramDelivery
	}
	p, err := decodeMap(raw)
	if err != nil {
		return errTelegramDelivery
	}
	storedChat, _ := telegramInt(p["telegram_chat_id"])
	if storedChat != chat || p["telegram_private_repayment"] != true {
		return errTelegramDelivery
	}
	if err = telegramBindCard(tx, draftID, chat, messageID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return a.telegramSyncCard(draftID, chat)
}

func (a *App) telegramRepaymentSelection(u telegramActor, sender, chat, messageID, updateID int64, data string) error {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || !validID(parts[1]) {
		return errors.New("Кнопка недоступна. Начните заново: /repay")
	}
	choice, err := strconv.Atoi(parts[2])
	if err != nil || choice < 0 {
		return errors.New("Кнопка недоступна")
	}
	tx, err := a.tx()
	if err != nil {
		return errors.New("Не удалось открыть возврат")
	}
	defer tx.Rollback()
	if err = telegramValidateRepaymentActor(tx, u, sender, chat); err != nil {
		return err
	}
	d, err := loadRepaymentDialog(tx, u.ID)
	if err != nil || !repaymentDialogMatches(d, u, sender) {
		return errors.New("Ввод завершён или истёк. Начните заново: /repay")
	}
	if d.Token != parts[1] {
		if d.LastUpdate == updateID {
			tx.Rollback()
			return a.telegramRepaymentPrompt(u, chat, messageID, d)
		}
		return errors.New("Кнопка устарела. Используйте последнее сообщение или /repay")
	}
	if d.Selected.ID != "" || d.DraftID != "" {
		return errors.New("Мерчант уже выбран. Для нового возврата: /repay")
	}
	if parts[0] == "rp" {
		if choice != d.Page-1 && !(choice == d.Page+1 && d.HasNext) {
			return errors.New("Страница недоступна")
		}
		if err = repaymentMerchantPage(tx, &d, choice); err != nil {
			return errors.New("Не удалось открыть страницу. Начните заново: /repay")
		}
	} else if parts[0] == "rm" {
		if choice >= len(d.Merchants) {
			return errors.New("Мерчант недоступен")
		}
		d.Selected = d.Merchants[choice]
		var name string
		if err = tx.QueryRow("SELECT name FROM merchants WHERE id=$1 AND active", d.Selected.ID).Scan(&name); err != nil || name != d.Selected.Name {
			return errors.New("Мерчант недоступен или изменился. Начните заново: /repay")
		}
		d.Token, d.Merchants = id(), nil
	} else {
		return errors.New("Кнопка недоступна")
	}
	if d.PromptMessageID == 0 {
		d.PromptMessageID = messageID
	}
	d.LastUpdate = updateID
	if err = saveRepaymentDialog(tx, u.ID, d); err != nil || tx.Commit() != nil {
		return errors.New("Не удалось сохранить выбор")
	}
	return a.telegramRepaymentPrompt(u, chat, messageID, d)
}

func (a *App) telegramRepaymentConfirm(u telegramActor, sender, chat int64, data string) (string, bool) {
	action, draftID, ok := strings.Cut(data, ":")
	if !ok || !validID(draftID) || (action != "rc" && action != "rr") {
		return "Неизвестная кнопка", false
	}
	tx, err := a.tx()
	if err != nil {
		return "Не удалось открыть возврат. Повторите подтверждение", false
	}
	defer tx.Rollback()
	if err = telegramValidateRepaymentActor(tx, u, sender, chat); err != nil {
		return err.Error(), false
	}
	var raw []byte
	var status string
	if err = tx.QueryRow("SELECT payload,status FROM drafts WHERE id=$1 AND kind='repayment' AND created_by=$2 FOR UPDATE", draftID, u.ID).Scan(&raw, &status); err != nil {
		return "Возврат недоступен", false
	}
	p, err := decodeMap(raw)
	storedSender, _ := telegramInt(p["telegram_sender_id"])
	storedChat, _ := telegramInt(p["telegram_chat_id"])
	if err != nil || p["telegram_private_repayment"] != true || storedSender != sender || storedChat != chat || str(p, "source_kind") != "cash" || str(p, "source_id") != u.CustodianID {
		return "Нет доступа к этому возврату", false
	}
	if status != "draft" {
		return "Возврат уже обработан", true
	}
	if action == "rr" {
		if _, err = tx.Exec("UPDATE drafts SET status='rejected' WHERE id=$1", draftID); err != nil {
			return "Не удалось отменить возврат", false
		}
		if _, err = tx.Exec("DELETE FROM telegram_repayment_dialogs WHERE user_id=$1 AND context->>'DraftID'=$2", u.ID, draftID); err != nil {
			return "Не удалось завершить ввод возврата", false
		}
		if err = txAudit(tx, u.ID, "telegram", "draft_reject", "repayment", draftID, "success", "Отменено отправителем", M{}); err != nil || tx.Commit() != nil {
			return "Не удалось отменить возврат", false
		}
		return "Возврат отменён. Списания нет", true
	}
	d, err := loadRepaymentDialog(tx, u.ID)
	if err != nil || !repaymentDialogMatches(d, u, sender) || d.DraftID != draftID || d.Selected.ID != str(p, "merchant_id") {
		return "Ввод завершён или истёк. Начните заново: /repay", false
	}
	if p["telegram_preview_sent"] != true {
		return "Предпросмотр ещё не доставлен. Повторите ввод суммы", false
	}
	group, configured := telegramAllowedGroup()
	storedGroup, _ := telegramInt(p["telegram_notification_chat_id"])
	if !configured || group != storedGroup {
		return "Группа для уведомлений изменилась. Начните заново: /repay", false
	}
	var name string
	if err = tx.QueryRow("SELECT name FROM merchants WHERE id=$1 AND active", str(p, "merchant_id")).Scan(&name); err != nil || name != str(p, "merchant_name") {
		return "Мерчант недоступен или изменился. Начните заново: /repay", false
	}
	cents, err := telegramInt(p["amount_cents"])
	if err != nil || cents <= 0 || str(p, "currency") != "RUB" {
		return "Некорректная сумма возврата", false
	}
	lines, err := a.eventLines(tx, "repayment", p, cents)
	if err != nil {
		a.logAudit(u.ID, "telegram", "draft_confirm", "repayment", draftID, "rejected", err.Error(), M{})
		return "Выдача не проведена. Проверьте доступные наличные с учётом резервов и повторите подтверждение", false
	}
	now := time.Now()
	if _, err = put(tx, "repayment", draftID, "draft:"+draftID, u.ID, now, now, lines, ""); err != nil {
		return "Не удалось провести возврат. Повторите подтверждение", false
	}
	if err := telegramCaptureBalance(tx, "repayment", p, u.ID); err != nil {
		return "Не удалось сохранить баланс", false
	}
	p["telegram_sender_confirmed"] = true
	if _, err = tx.Exec("UPDATE drafts SET status='posted',payload=$1,confirmed_by=$2,confirmed_at=$3,confirmed_amount_cents=$4 WHERE id=$5", encode(p), u.ID, now, cents, draftID); err != nil {
		return "Не удалось сохранить возврат", false
	}
	notification := "Выдача мерчанту\nМерчант: " + name + "\nСумма: " + telegramMoney(cents) + "\nВыдал: Тони Сопрано"
	notification += "\nБаланс: " + telegramMoney(p["telegram_balance_after_cents"].(int64))
	if _, err = tx.Exec("INSERT INTO telegram_repayment_notifications(draft_id,chat_id,message) VALUES($1,$2,$3)", draftID, group, notification); err != nil {
		return "Не удалось сохранить уведомление. Возврат не проведён", false
	}
	if _, err = tx.Exec("DELETE FROM telegram_repayment_dialogs WHERE user_id=$1 AND context->>'DraftID'=$2", u.ID, draftID); err != nil {
		return "Не удалось завершить ввод возврата", false
	}
	if err = txAudit(tx, u.ID, "telegram", "draft_confirm", "repayment", draftID, "success", "", M{"confirmed_cents": cents, "merchant_id": str(p, "merchant_id"), "custodian_id": u.CustodianID}); err != nil || tx.Commit() != nil {
		return "Не удалось сохранить возврат. Повторите подтверждение", false
	}
	return "Выдача подтверждена. Деньги списаны с наличных Тони. Уведомление поставлено в очередь группы «Касса снятия»", true
}

// Returns true only for the private repayment flow, leaving transfer receipts
// and their /start and /pending commands on the existing path.
func (a *App) telegramRepaymentPrivateUpdate(w http.ResponseWriter, x telegramUpdate, u telegramActor, sender, chat int64) bool {
	if x.CallbackQuery != nil {
		data := x.CallbackQuery.Data
		if !strings.HasPrefix(data, "rp:") && !strings.HasPrefix(data, "rm:") && !strings.HasPrefix(data, "rc:") && !strings.HasPrefix(data, "rr:") {
			return false
		}
		message, remove := "", false
		if strings.HasPrefix(data, "rp:") || strings.HasPrefix(data, "rm:") {
			err := a.telegramRepaymentSelection(u, sender, chat, x.CallbackQuery.Message.MessageID, x.UpdateID, data)
			if errors.Is(err, errTelegramDelivery) {
				http.Error(w, "telegram delivery failed", 500)
				return true
			}
			message = "Выбор сохранён"
			if err != nil {
				message = err.Error()
			}
		} else {
			message, remove = a.telegramRepaymentConfirm(u, sender, chat, data)
		}
		if !remove && message != "Выбор сохранён" {
			a.logAudit(u.ID, "telegram", "repayment_callback", "command", "", "rejected", message, M{"update_id": x.UpdateID})
		}
		if remove {
			if err := a.telegramRepaymentResult(u, chat, x.CallbackQuery.Message.MessageID, data); err != nil {
				http.Error(w, "telegram delivery failed", 500)
				return true
			}
		}
		if err := a.telegramAnswer(x.CallbackQuery.ID, message); err != nil {
			http.Error(w, "telegram delivery failed", 500)
			return true
		}
		w.WriteHeader(http.StatusOK)
		return true
	}
	if x.Message == nil {
		return false
	}
	command := telegramMessageCommand(x.Message.Text)
	if command != "repay" && command != "cancel" {
		if strings.HasPrefix(strings.TrimSpace(x.Message.Text), "/") || u.CustodianID != telegramTonyCustodianID {
			return false
		}
		var waiting bool
		if err := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM telegram_repayment_dialogs WHERE user_id=$1 AND expires_at>now())", u.ID).Scan(&waiting); err != nil {
			http.Error(w, "database unavailable", 500)
			return true
		}
		// A consumed amount update may be retried after /cancel or expiry;
		// deduplication in telegramRepaymentMessage prevents another posting.
		if !waiting {
			return false
		}
	}
	if telegramSensitiveNumber.MatchString(x.Message.Text) {
		if err := a.telegramReply(chat, "Не отправляйте номера карт или другие секреты. Для возврата нужна только сумма.", nil); err != nil {
			http.Error(w, "telegram delivery failed", 500)
			return true
		}
		w.WriteHeader(http.StatusOK)
		return true
	}
	if err := a.telegramRepaymentMessage(u, x.Message, x.UpdateID); err != nil {
		if !errors.Is(err, errTelegramDelivery) {
			a.logAudit(u.ID, "telegram", "repayment_input", "command", "", "rejected", err.Error(), M{"update_id": x.UpdateID})
		}
		if errors.Is(err, errTelegramDelivery) || a.telegramReportInputError(u, chat, x.UpdateID, "repay", err) != nil {
			http.Error(w, "telegram delivery failed", 500)
			return true
		}
	} else if telegramMessageCommand(x.Message.Text) != "repay" {
		if err := a.telegramResolveInputErrors(u, chat, "repay"); err != nil {
			http.Error(w, "database unavailable", 500)
			return true
		}
		_ = a.telegramCleanupInputErrors(10)
	}
	w.WriteHeader(http.StatusOK)
	return true
}
