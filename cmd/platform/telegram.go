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
	if x.Message != nil && !strings.HasPrefix(strings.TrimSpace(x.Message.Text), "/") && !telegramSensitiveNumber.MatchString(x.Message.Text) {
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
		message, remove := a.telegramCallback(u, chat, x.CallbackQuery.Data)
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

func (a *App) telegramHandleMessage(u telegramActor, message *telegramMessage, updateID int64) error {
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
			return a.telegramReply(message.Chat.ID, "Выберите /withdraw или /expense в меню. В обеих командах можно указать несколько операций — по одной в строке или через ;. Разрешены расходы «Прогрев» и «Банк. Комиссия». Чтобы выйти из ввода, отправьте /cancel.", nil)
		}
		return a.telegramReply(message.Chat.ID, "Выберите /withdraw или /expense в меню и отправьте данные. Для снятия: 7898 100к/200. Для расходов: по одной строке вида 7898 прогрев 230 или несколько строк сразу. Чтобы выйти из ввода, отправьте /cancel.", nil)
	}
	args := ""
	if strings.HasPrefix(words[0], "/") {
		if command != "withdraw" && command != "expense" {
			return errors.New("Доступны команды /start, /expense, /withdraw и /cancel")
		}
		if len(words) > 1 {
			args = strings.TrimSpace(text[len(words[0]):])
		}
	} else {
		e := a.db.QueryRow("SELECT command FROM telegram_dialogs WHERE user_id=$1 AND expires_at>now()", u.ID).Scan(&command)
		if e != nil {
			return errors.New("Сначала выберите команду /withdraw или /expense в меню")
		}
		args = text
	}
	if args == "" {
		_, e := a.db.Exec("INSERT INTO telegram_dialogs(user_id,command,expires_at) VALUES($1,$2,now()+interval '10 minutes') ON CONFLICT(user_id) DO UPDATE SET command=EXCLUDED.command,expires_at=EXCLUDED.expires_at,updated_at=now()", u.ID, command)
		if e != nil {
			return errors.New("Не удалось начать ввод")
		}
		if command == "withdraw" {
			return a.telegramReply(message.Chat.ID, "Введите снятия по одному в строке: последние 4 цифры карты, сумма снятия и остаток.\n\nНапример:\n7898 100к/200\n4567 50к/100\n\nЧтобы выйти: /cancel", nil)
		}
		return a.telegramReply(message.Chat.ID, "Введите последние 4 цифры карты, тип расхода и сумму. Несколько расходов укажите по одному в строке или через ;\n\nНапример:\n7898 прогрев 230\n7898 банк. комиссия 25,50\n\nЧтобы выйти: /cancel", nil)
	}
	payload := M{"telegram_confirmation_required": true, "telegram_sender_confirmed": false, "telegram_preview_sent": false, "telegram_chat_id": message.Chat.ID, "telegram_raw": text, "telegram_actor_name": u.Name}
	if command == "withdraw" {
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
			items = append(items, M{"card_id": intent.CardID, "amount_cents": intent.Amount, "observed_cents": intent.Observed, "telegram_mask": intent.Mask})
		}
		payload["amount"] = rub(total)
		payload["amount_cents"] = total
		payload["custodian_id"] = u.CustodianID
		payload["withdrawal_items"] = items
	} else {
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
	_, e := a.db.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,$2,$3,$4,$5)", draftID, commandKind(command), encode(payload), u.ID, fmt.Sprintf("telegram:%d", updateID))
	if e != nil {
		return errors.New("Черновик не сохранён; проверьте карту и повторите ввод")
	}
	_, _ = a.db.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1", u.ID)
	auditDetail := M{"update_id": updateID}
	if command == "expense" {
		items, _ := telegramExpenseItems(payload)
		auditDetail["item_count"] = len(items)
	} else {
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
	if itemsErr == nil && len(items) == 1 {
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
			details = "Автор: " + actor + "\nКарта: " + str(withdrawals[0], "telegram_mask") + "\nСумма снятия: " + telegramMoney(itemAmount) + "\nОстаток: " + telegramMoney(observed)
		} else {
			heading = "Подтвердите снятия и остатки по картам"
			lines := []string{"Автор: " + actor}
			for i, item := range withdrawals {
				itemAmount, _ := telegramInt(item["amount_cents"])
				observed, _ := telegramInt(item["observed_cents"])
				lines = append(lines, fmt.Sprintf("%d. %s · снято %s · остаток %s", i+1, str(item, "telegram_mask"), telegramMoney(itemAmount), telegramMoney(observed)))
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
