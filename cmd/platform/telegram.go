package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type telegramMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
}

type telegramUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *telegramMessage `json:"message"`
	CallbackQuery *struct {
		ID      string           `json:"id"`
		Data    string           `json:"data"`
		Message *telegramMessage `json:"message"`
		From    struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"callback_query"`
}

type telegramActor struct {
	User
	CustodianID string
}

func telegramFieldRole(role string) bool {
	return role == "collector" || role == "operator"
}

var errTelegramDelivery = errors.New("Telegram временно недоступен")

// Reject full card numbers both as one string and in groups separated by spaces or hyphens.
var telegramSensitiveNumber = regexp.MustCompile(`(^|[^0-9])(?:[0-9][ -]?){11,18}[0-9]([^0-9]|$)`)

func telegramAllowedGroup() (int64, bool) {
	chatID, e := strconv.ParseInt(strings.TrimSpace(os.Getenv("TELEGRAM_ALLOWED_CHAT_ID")), 10, 64)
	return chatID, e == nil && chatID < 0
}

func telegramGroupAllowed(chatID int64, chatType string) bool {
	allowed, ok := telegramAllowedGroup()
	return ok && chatID == allowed && (chatType == "group" || chatType == "supergroup")
}

func telegramCommand(token string) string {
	command := strings.TrimPrefix(token, "/")
	command, _, _ = strings.Cut(command, "@")
	return strings.ToLower(command)
}

func telegramMessageCommand(text string) string {
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 {
		return ""
	}
	return telegramCommand(words[0])
}

func (a *App) telegram(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("TELEGRAM_WEBHOOK_SECRET") == "" || telegramToken() == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost || r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != os.Getenv("TELEGRAM_WEBHOOK_SECRET") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if e != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var x telegramUpdate
	if e = json.Unmarshal(raw, &x); e != nil || x.UpdateID <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var sender, chat int64
	var chatType string
	if x.Message != nil {
		sender, chat, chatType = x.Message.From.ID, x.Message.Chat.ID, x.Message.Chat.Type
	} else if x.CallbackQuery != nil && x.CallbackQuery.Message != nil {
		sender, chat, chatType = x.CallbackQuery.From.ID, x.CallbackQuery.Message.Chat.ID, x.CallbackQuery.Message.Chat.Type
	}
	if sender <= 0 || chat == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	if chatType == "private" && chat == sender {
		a.telegramPrivateUpdate(w, x, sender, chat)
		return
	}
	if !telegramGroupAllowed(chat, chatType) {
		if x.CallbackQuery != nil {
			_ = a.telegramAnswer(x.CallbackQuery.ID, "Эта группа не подключена")
		} else if x.Message != nil && (telegramMessageCommand(x.Message.Text) == "start" || telegramMessageCommand(x.Message.Text) == "help") {
			if chatType == "group" || chatType == "supergroup" {
				_ = a.telegramReply(chat, fmt.Sprintf("Эта группа ещё не подключена. Её Telegram chat ID: %d", chat), nil)
			} else {
				_ = a.telegramReply(chat, "Бот работает только в подключённой рабочей группе.", nil)
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if x.Message != nil && !strings.HasPrefix(strings.TrimSpace(x.Message.Text), "/") && telegramMessageCommand(x.Message.Text) != "зп" && !telegramSensitiveNumber.MatchString(x.Message.Text) {
		var waiting bool
		e = a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM telegram_dialogs d JOIN users u ON u.id=d.user_id WHERE u.telegram_id=$1 AND u.active AND d.expires_at>now())", sender).Scan(&waiting)
		if e != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if !waiting {
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	// Persist update identity for deduplication and audit, never arbitrary chat text.
	recorded := encode(M{"update_id": x.UpdateID, "from_id": sender, "chat_id": chat})
	if x.Message != nil {
		recorded = encode(M{"update_id": x.UpdateID, "from_id": sender, "chat_id": chat, "message_id": x.Message.MessageID, "type": "message", "redacted": telegramSensitiveNumber.MatchString(x.Message.Text)})
	} else if x.CallbackQuery != nil {
		recorded = encode(M{"update_id": x.UpdateID, "from_id": sender, "chat_id": chat, "type": "callback_query"})
	}
	res, e := a.db.Exec("INSERT INTO telegram_updates(update_id,raw_update) VALUES($1,$2) ON CONFLICT DO NOTHING", x.UpdateID, recorded)
	if e != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	inserted, _ := res.RowsAffected()
	if x.Message != nil && telegramSensitiveNumber.MatchString(x.Message.Text) {
		a.telegramDeleteMessage(chat, x.Message.MessageID)
		_ = a.telegramReply(chat, "Не отправляйте полный номер карты или другие секреты. В команде нужны только последние 4 цифры.", nil)
		w.WriteHeader(http.StatusOK)
		return
	}
	var u telegramActor
	e = a.db.QueryRow("SELECT id,login,name,role,COALESCE(custodian_id::text,'') FROM users WHERE telegram_id=$1 AND active", sender).Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.CustodianID)
	if e != nil || (!telegramFieldRole(u.Role) && u.Role != "chief") || u.CustodianID == "" {
		_ = a.telegramReply(chat, "Доступ не настроен. Попросите системного администратора связать ваш Telegram ID и ответственного за наличные.", nil)
		w.WriteHeader(http.StatusOK)
		return
	}
	if x.CallbackQuery != nil {
		var message string
		var remove bool
		if strings.HasPrefix(x.CallbackQuery.Data, "tr:") || strings.HasPrefix(x.CallbackQuery.Data, "ta:") {
			message, remove = a.telegramTransferInputCallback(u, chat, x.CallbackQuery.Message.MessageID, x.UpdateID, x.CallbackQuery.Data)
			if message == errTelegramDelivery.Error() {
				http.Error(w, "telegram delivery failed", http.StatusInternalServerError)
				return
			}
		} else {
			message, remove = a.telegramCallback(u, chat, x.CallbackQuery.Message.MessageID, x.CallbackQuery.Data)
		}
		if e := a.telegramAnswer(x.CallbackQuery.ID, message); e != nil {
			http.Error(w, "telegram delivery failed", http.StatusInternalServerError)
			return
		}
		if remove {
			a.telegramRemoveButtons(chat, x.CallbackQuery.Message.MessageID)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if inserted == 0 {
		if e = a.telegramRetryPreview(x.UpdateID, chat); e != nil {
			http.Error(w, "telegram delivery failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if e = a.telegramHandleMessage(u, x.Message, x.UpdateID); e != nil {
		if !errors.Is(e, errTelegramDelivery) {
			a.logAudit(u.ID, "telegram", "message", "command", "", "rejected", e.Error(), M{"update_id": x.UpdateID})
		}
		if errors.Is(e, errTelegramDelivery) || a.telegramReply(chat, e.Error(), nil) != nil {
			http.Error(w, "telegram delivery failed", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (a *App) telegramHandleMessage(u telegramActor, message *telegramMessage, updateID int64, expectedTransferToken ...string) error {
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return nil
	}
	words := strings.Fields(text)
	command := telegramCommand(words[0])
	if command == "cancel" {
		res, e := a.db.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1", u.ID)
		if e != nil {
			return errors.New("Не удалось отменить ввод")
		}
		cancelled, _ := res.RowsAffected()
		a.logAudit(u.ID, "telegram", "input_cancel", "command", "", "success", "", M{"update_id": updateID, "active": cancelled == 1})
		if cancelled == 0 {
			return a.telegramReply(message.Chat.ID, "Активного ввода нет.", nil)
		}
		return a.telegramReply(message.Chat.ID, "Ввод отменён. Обычные сообщения больше не обрабатываются.", nil)
	}
	if command == "start" {
		if telegramFieldRole(u.Role) {
			return a.telegramReply(message.Chat.ID, "Доступны /cards_balance, /my_balance, /withdraw и /expense. Сборщику также доступен /transfer для перевода наличных другому сборщику. Для зарплаты: зп 50000 комментарий. Чтобы выйти из ввода, отправьте /cancel.", nil)
		}
		return a.telegramReply(message.Chat.ID, "Доступны /cards_balance, /my_balance, /withdraw и /expense. Для снятия: 7898 100к/200. Для расходов: по одной строке вида 7898 прогрев 230 или несколько строк сразу. Для зарплаты: зп 50000 комментарий. Чтобы выйти из ввода, отправьте /cancel.", nil)
	}
	if command == "cards_balance" {
		return a.telegramCardsBalance(u, message.Chat.ID, updateID)
	}
	if command == "my_balance" {
		return a.telegramMyBalance(u, message.Chat.ID, updateID)
	}
	args := ""
	if command == "зп" {
		command = "salary"
		args = strings.TrimSpace(text[len(words[0]):])
	} else if strings.HasPrefix(words[0], "/") {
		if command != "withdraw" && command != "expense" && command != "transfer" {
			return errors.New("Доступны команды /start, /cards_balance, /my_balance, /expense, /withdraw, /transfer и /cancel")
		}
		if len(words) > 1 {
			args = strings.TrimSpace(text[len(words[0]):])
		}
	} else {
		e := a.db.QueryRow("SELECT command FROM telegram_dialogs WHERE user_id=$1 AND expires_at>now()", u.ID).Scan(&command)
		if e != nil {
			return errors.New("Сначала выберите команду /withdraw, /expense или /transfer в меню")
		}
		args = text
	}
	if command == "transfer" && u.Role != "collector" {
		return errors.New("Перевод доступен только сборщику")
	}
	if command == "salary" && args == "" {
		return errors.New("Формат: зп 50000 комментарий")
	}
	if args == "" {
		_, e := a.db.Exec("INSERT INTO telegram_dialogs(user_id,command,expires_at) VALUES($1,$2,now()+interval '10 minutes') ON CONFLICT(user_id) DO UPDATE SET command=EXCLUDED.command,expires_at=EXCLUDED.expires_at,updated_at=now(),context='{}'::jsonb", u.ID, command)
		if e != nil {
			return errors.New("Не удалось начать ввод")
		}
		if command == "transfer" {
			return a.telegramTransferOptions(u, message.Chat.ID)
		}
		if command == "withdraw" {
			return a.telegramReply(message.Chat.ID, "Введите снятия по одному в строке: последние 4 цифры карты, сумма снятия и остаток.\n\nНапример:\n7898 100к/200\n3338 100к/143500 комса 2400\n4567 50к/100\n\nЧтобы выйти: /cancel", nil)
		}
		return a.telegramReply(message.Chat.ID, "Введите последние 4 цифры карты, тип расхода и сумму. Несколько расходов укажите по одному в строке или через ;\n\nНапример:\n7898 прогрев 230\n7898 комса 25,50\n\nЧтобы выйти: /cancel", nil)
	}
	transferToken := ""
	payload := M{"telegram_confirmation_required": true, "telegram_sender_confirmed": false, "telegram_preview_sent": false, "telegram_chat_id": message.Chat.ID, "telegram_raw": text, "telegram_actor_name": u.Name}
	if command == "transfer" {
		var recipient telegramTransferRecipient
		fields := strings.Fields(args)
		amount := ""
		if !strings.HasPrefix(words[0], "/") {
			if len(fields) != 1 {
				return errors.New("Отправьте только сумму перевода, например: 1000,00")
			}
			dialog, err := a.telegramTransferDialog(u)
			if err != nil || dialog.Recipient.ID == "" {
				return errors.New("Сначала выберите получателя кнопкой /transfer")
			}
			if len(expectedTransferToken) > 0 && expectedTransferToken[0] != dialog.Token {
				return errors.New("Ввод уже изменился. Повторите /transfer")
			}
			transferToken = dialog.Token
			recipient = dialog.Recipient
			amount = fields[0]
		} else {
			if len(fields) != 2 {
				return errors.New("Выберите получателя кнопкой /transfer и отправьте сумму")
			}
			choice, err := strconv.Atoi(fields[0])
			if err != nil || choice < 1 {
				return errors.New("Выберите номер получателя из списка /transfer")
			}
			recipients, err := a.telegramTransferRecipients(u)
			if err != nil || choice > len(recipients) {
				return errors.New("Получатель недоступен. Повторите /transfer")
			}
			recipient = recipients[choice-1]
			amount = fields[1]
		}
		name, err := a.telegramTransferRecipientActive(recipient.ID)
		if err != nil {
			return err
		}
		cents, err := telegramAmount(amount)
		if err != nil || cents <= 0 {
			return errors.New("Укажите положительную сумму перевода")
		}
		var recipientUser string
		var recipientTelegram int64
		if err = a.db.QueryRow("SELECT id,COALESCE(telegram_id,0) FROM users WHERE custodian_id=$1 AND active AND role='collector'", recipient.ID).Scan(&recipientUser, &recipientTelegram); err != nil {
			return errors.New("Получатель недоступен")
		}
		payload["telegram_recipient_confirmation_required"] = true
		payload["telegram_sender_id"] = message.From.ID
		payload["telegram_recipient_user_id"] = recipientUser
		payload["telegram_recipient_id"] = recipientTelegram
		payload["amount"] = rub(cents)
		payload["amount_cents"] = cents
		payload["from_custodian_id"] = u.CustodianID
		payload["to_custodian_id"] = recipient.ID
		payload["to_custodian_name"] = name
	} else if command == "salary" {
		item, err := telegramSalary(args, u.CustodianID)
		if err != nil {
			return err
		}
		for key, value := range item {
			payload[key] = value
		}
		payload["expense_items"] = []M{item}
	} else if command == "withdraw" {
		intents, e := a.telegramParseWithdrawals(u, args)
		if e != nil {
			return e
		}
		items := make([]M, 0, len(intents))
		var total int64
		for _, intent := range intents {
			if intent.Amount > math.MaxInt64-total {
				return errors.New("Общая сумма снятий слишком велика")
			}
			total += intent.Amount
			items = append(items, M{"card_id": intent.CardID, "amount_cents": intent.Amount, "observed_cents": intent.Observed, "telegram_mask": intent.Mask, "bank_fee_cents": intent.BankFee})
		}
		payload["amount"] = rub(total)
		payload["amount_cents"] = total
		payload["custodian_id"] = u.CustodianID
		payload["withdrawal_items"] = items
	} else if command == "expense" {
		intents, e := a.telegramParseExpenses(u, args)
		if e != nil {
			return e
		}
		items := make([]M, 0, len(intents))
		var total int64
		for _, intent := range intents {
			if intent.Amount > math.MaxInt64-total {
				return errors.New("Общая сумма расходов слишком велика")
			}
			total += intent.Amount
			items = append(items, M{"card_id": intent.CardID, "source_kind": "card", "source_id": intent.CardID, "category": intent.Category, "amount_cents": intent.Amount, "telegram_mask": intent.Mask})
		}
		payload["amount"] = rub(total)
		payload["amount_cents"] = total
		payload["expense_items"] = items
	}
	draftID := id()
	var e error
	if transferToken != "" {
		tx, err := a.tx()
		if err != nil {
			return errors.New("Не удалось сохранить перевод")
		}
		defer tx.Rollback()
		var claimed bool
		err = tx.QueryRow("DELETE FROM telegram_dialogs WHERE user_id=$1 AND command='transfer' AND expires_at>now() AND context->>'Token'=$2 AND context->'Recipient'->>'ID'=$3 RETURNING true", u.ID, transferToken, str(payload, "to_custodian_id")).Scan(&claimed)
		if err != nil || !claimed {
			return errors.New("Ввод уже завершён или изменился. Повторите /transfer")
		}
		_, e = tx.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,$2,$3,$4,$5)", draftID, commandKind(command), encode(payload), u.ID, fmt.Sprintf("telegram:%d", updateID))
		if e == nil {
			e = tx.Commit()
		}
	} else {
		_, e = a.db.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,$2,$3,$4,$5)", draftID, commandKind(command), encode(payload), u.ID, fmt.Sprintf("telegram:%d", updateID))
		if e == nil {
			_, _ = a.db.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1", u.ID)
		}
	}
	if e != nil {
		return errors.New("Черновик не сохранён; повторите ввод")
	}

	auditDetail := M{"update_id": updateID}
	if commandKind(command) == "expense" {
		items, _ := telegramExpenseItems(payload)
		auditDetail["item_count"] = len(items)
	} else if command == "withdraw" {
		items, _ := telegramWithdrawalItems(payload)
		auditDetail["item_count"] = len(items)
	}
	a.logAudit(u.ID, "telegram", "draft_create", commandKind(command), draftID, "success", "", auditDetail)
	return a.telegramSendPreview(draftID, message.Chat.ID, payload)
}

func commandKind(command string) string {
	if command == "withdraw" {
		return "withdrawal"
	}
	if command == "transfer" {
		return "transfer"
	}
	return "expense"
}

func (a *App) telegramRetryPreview(updateID, chat int64) error {
	var draftID string
	var raw []byte
	var status string
	e := a.db.QueryRow("SELECT id,payload,status FROM drafts WHERE idempotency_key=$1", fmt.Sprintf("telegram:%d", updateID)).Scan(&draftID, &raw, &status)
	if e != nil || status != "draft" {
		return nil
	}
	p, e := decodeMap(raw)
	if e == nil && p["telegram_preview_sent"] != true {
		return a.telegramSendPreview(draftID, chat, p)
	}
	return nil
}

func (a *App) telegramSendPreview(draftID string, chat int64, p M) error {
	amountCents, _ := telegramInt(p["amount_cents"])
	actor := str(p, "telegram_actor_name")
	var heading, details string
	items, itemsErr := telegramExpenseItems(p)
	if str(p, "to_custodian_id") != "" {
		heading = "Подтвердите перевод наличных"
		details = "Отправитель: " + actor + "\nПолучатель: " + str(p, "to_custodian_name") + "\nСумма: " + telegramMoney(amountCents) + "\nПосле вашего подтверждения сумма будет зарезервирована до подтверждения получателем или главным администратором."
	} else if itemsErr == nil && len(items) == 1 && str(items[0], "source_kind") == "cash" {
		heading = "Подтвердите расход «Зарплата»"
		details = "Автор: " + actor + "\nИсточник: наличные автора\nСумма: " + telegramMoney(amountCents) + "\nКомментарий: " + str(items[0], "comment")
	} else if itemsErr == nil && len(items) == 1 {
		itemAmount, _ := telegramInt(items[0]["amount_cents"])
		heading = "Подтвердите расход по карте"
		details = "Автор: " + actor + "\nКарта: " + str(items[0], "telegram_mask") + "\nСумма расхода: " + telegramMoney(itemAmount) + "\nКатегория: " + telegramCategoryName(str(items[0], "category"))
	} else if itemsErr == nil {
		heading = "Подтвердите расходы по картам"
		lines := []string{"Автор: " + actor}
		for i, item := range items {
			itemAmount, _ := telegramInt(item["amount_cents"])
			lines = append(lines, fmt.Sprintf("%d. %s · %s · %s", i+1, str(item, "telegram_mask"), telegramCategoryName(str(item, "category")), telegramMoney(itemAmount)))
		}
		lines = append(lines, "", "Итого: "+telegramMoney(amountCents))
		details = strings.Join(lines, "\n")
	} else {
		withdrawals, err := telegramWithdrawalItems(p)
		if err != nil {
			return err
		}
		if len(withdrawals) == 1 {
			itemAmount, _ := telegramInt(withdrawals[0]["amount_cents"])
			observed, _ := telegramInt(withdrawals[0]["observed_cents"])
			heading = "Подтвердите снятие и остаток по карте"
			details = "Автор: " + actor + "\nКарта: " + str(withdrawals[0], "telegram_mask") + "\nСумма снятия: " + telegramMoney(itemAmount) + "\nОстаток: " + telegramMoney(observed) + telegramWithdrawalFeePreview(withdrawals[0])
		} else {
			heading = "Подтвердите снятия и остатки по картам"
			lines := []string{"Автор: " + actor}
			for i, item := range withdrawals {
				itemAmount, _ := telegramInt(item["amount_cents"])
				observed, _ := telegramInt(item["observed_cents"])
				lines = append(lines, fmt.Sprintf("%d. %s · снято %s · остаток %s", i+1, str(item, "telegram_mask"), telegramMoney(itemAmount), telegramMoney(observed))+telegramWithdrawalFeePreview(item))
			}
			lines = append(lines, "", "Итого снято: "+telegramMoney(amountCents))
			details = strings.Join(lines, "\n")
		}
	}
	keyboard := M{"inline_keyboard": []interface{}{[]interface{}{M{"text": "Подтвердить", "callback_data": "confirm:" + draftID}, M{"text": "Отклонить", "callback_data": "reject:" + draftID}}}}
	if e := a.telegramReply(chat, heading+"\n\n"+details, keyboard); e != nil {
		return e
	}
	_, e := a.db.Exec("UPDATE drafts SET payload=jsonb_set(payload,'{telegram_preview_sent}','true'::jsonb) WHERE id=$1 AND status='draft'", draftID)
	return e
}

func telegramInt(v interface{}) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case json.Number:
		return x.Int64()
	default:
		return 0, errors.New("неверная сумма")
	}
}

func telegramExpenseItems(p M) ([]M, error) {
	if raw, ok := p["expense_items"]; ok {
		var items []M
		switch list := raw.(type) {
		case []M:
			items = list
		case []interface{}:
			items = make([]M, 0, len(list))
			for _, value := range list {
				item, ok := value.(map[string]interface{})
				if !ok {
					return nil, errors.New("список расходов повреждён")
				}
				items = append(items, M(item))
			}
		default:
			return nil, errors.New("список расходов повреждён")
		}
		if len(items) == 0 || len(items) > telegramBatchLimit {
			return nil, errors.New("список расходов повреждён")
		}
		return items, nil
	}
	// Backward compatibility for drafts created before batch input was introduced.
	if str(p, "category") != "" {
		return []M{{"card_id": str(p, "card_id"), "source_kind": str(p, "source_kind"), "source_id": str(p, "source_id"), "category": str(p, "category"), "amount_cents": p["amount_cents"], "telegram_mask": str(p, "telegram_mask")}}, nil
	}
	return nil, errors.New("это не расход")
}

func telegramWithdrawalItems(p M) ([]M, error) {
	if raw, ok := p["withdrawal_items"]; ok {
		var items []M
		switch list := raw.(type) {
		case []M:
			items = list
		case []interface{}:
			items = make([]M, 0, len(list))
			for _, value := range list {
				item, ok := value.(map[string]interface{})
				if !ok {
					return nil, errors.New("список снятий повреждён")
				}
				items = append(items, M(item))
			}
		default:
			return nil, errors.New("список снятий повреждён")
		}
		if len(items) == 0 || len(items) > telegramBatchLimit {
			return nil, errors.New("список снятий повреждён")
		}
		return items, nil
	}
	// Backward compatibility for drafts created before batch input was introduced.
	if str(p, "card_id") != "" && p["observed_cents"] != nil {
		return []M{{"card_id": str(p, "card_id"), "amount_cents": p["amount_cents"], "observed_cents": p["observed_cents"], "telegram_mask": str(p, "telegram_mask")}}, nil
	}
	return nil, errors.New("это не снятие")
}

func (a *App) telegramReply(chat int64, message string, markup interface{}) error {
	body := M{"chat_id": chat, "text": message}
	if markup != nil {
		body["reply_markup"] = markup
	}
	if e := a.telegramCall("sendMessage", body); e != nil {
		return errTelegramDelivery
	}
	return nil
}

func (a *App) telegramAnswer(callbackID, message string) error {
	return a.telegramCall("answerCallbackQuery", M{"callback_query_id": callbackID, "text": message})
}

func (a *App) telegramRemoveButtons(chat, messageID int64) {
	_ = a.telegramCall("editMessageReplyMarkup", M{"chat_id": chat, "message_id": messageID, "reply_markup": M{"inline_keyboard": []interface{}{}}})
}

func (a *App) telegramDeleteMessage(chat, messageID int64) {
	_ = a.telegramCall("deleteMessage", M{"chat_id": chat, "message_id": messageID})
}

func telegramWithdrawalFee(item M) (int64, error) {
	if item["bank_fee_cents"] == nil {
		return 0, nil // Older drafts have no attached fee.
	}
	fee, err := telegramInt(item["bank_fee_cents"])
	if err != nil || fee < 0 {
		return 0, errors.New("Некорректная сумма банковской комиссии")
	}
	return fee, nil
}

func telegramWithdrawalFeePreview(item M) string {
	fee, _ := telegramWithdrawalFee(item)
	if fee == 0 {
		return ""
	}
	return "\nБанк. Комиссия с этой карты: " + telegramMoney(fee)
}
