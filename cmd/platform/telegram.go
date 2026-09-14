package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
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

var errTelegramDelivery = errors.New("Telegram временно недоступен")

// Reject full card numbers both as one string and in groups separated by spaces or hyphens.
var telegramSensitiveNumber = regexp.MustCompile(`(^|[^0-9])(?:[0-9][ -]?){11,18}[0-9]([^0-9]|$)`)

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
	var private bool
	if x.Message != nil {
		sender, chat, private = x.Message.From.ID, x.Message.Chat.ID, x.Message.Chat.Type == "private"
	} else if x.CallbackQuery != nil && x.CallbackQuery.Message != nil {
		sender, chat, private = x.CallbackQuery.From.ID, x.CallbackQuery.Message.Chat.ID, x.CallbackQuery.Message.Chat.Type == "private"
	}
	if !private || sender <= 0 || chat <= 0 {
		w.WriteHeader(http.StatusOK)
		return
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
		_ = a.telegramReply(chat, "Не отправляйте полный номер карты или другие секреты. В команде нужны только последние 4 цифры.", nil)
		w.WriteHeader(http.StatusOK)
		return
	}
	var u telegramActor
	e = a.db.QueryRow("SELECT id,login,name,role,COALESCE(custodian_id::text,'') FROM users WHERE telegram_id=$1 AND active", sender).Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.CustodianID)
	if e != nil || (u.Role != "collector" && u.Role != "chief") || u.CustodianID == "" {
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
	command := strings.TrimPrefix(words[0], "/")
	if command == "start" || command == "help" {
		if u.Role == "collector" {
			return a.telegramReply(message.Chat.ID, "Выберите /withdraw или /expense в меню. После выбора отправьте данные одним сообщением. Разрешены расходы «Прогрев» и «Банк. Комиссия».", nil)
		}
		return a.telegramReply(message.Chat.ID, "Выберите /withdraw, /expense или /balance в меню и отправьте данные. Для снятия: 7898 100к/200. Для расхода: 7898 прогрев 230.", nil)
	}
	if command == "balance" {
		if u.Role != "chief" {
			return errors.New("Ввод остатков доступен только главному администратору")
		}
		if len(words) != 3 {
			return errors.New("Формат: /balance <последние 4 цифры карты> <остаток>")
		}
		card, _, e := a.telegramCard(u, words[1])
		if e != nil {
			return e
		}
		v, e := nonnegative(words[2])
		if e != nil {
			return e
		}
		_, e = a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,$3,now(),$4,'telegram')", id(), card, v, u.ID)
		if e != nil {
			return errors.New("Не удалось сохранить остаток")
		}
		a.logAudit(u.ID, "telegram", "observation", "card", card, "success", "", M{})
		return a.telegramReply(message.Chat.ID, "Остаток карты сохранён как наблюдение. Денежной проводки нет.", nil)
	}
	args := ""
	if strings.HasPrefix(words[0], "/") {
		if command != "withdraw" && command != "expense" {
			return errors.New("Выберите /withdraw или /expense в меню")
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
			return a.telegramReply(message.Chat.ID, "Введите последние 4 цифры карты, сумму снятия и остаток: 7898 100к/200", nil)
		}
		return a.telegramReply(message.Chat.ID, "Введите последние 4 цифры карты, тип расхода и сумму: 7898 прогрев 230", nil)
	}
	intent, e := a.telegramParse(u, command, args)
	if e != nil {
		return e
	}
	payload := M{"card_id": intent.CardID, "amount": rub(intent.Amount), "amount_cents": intent.Amount, "telegram_confirmation_required": true, "telegram_sender_confirmed": false, "telegram_preview_sent": false, "telegram_chat_id": message.Chat.ID, "telegram_raw": text, "telegram_mask": intent.Mask}
	if command == "withdraw" {
		payload["custodian_id"] = u.CustodianID
		payload["observed_cents"] = intent.Observed
	} else {
		payload["source_kind"] = "card"
		payload["source_id"] = intent.CardID
		payload["category"] = intent.Category
	}
	draftID := id()
	_, e = a.db.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,$2,$3,$4,$5)", draftID, commandKind(command), encode(payload), u.ID, fmt.Sprintf("telegram:%d", updateID))
	if e != nil {
		return errors.New("Черновик не сохранён; проверьте карту и повторите ввод")
	}
	_, _ = a.db.Exec("DELETE FROM telegram_dialogs WHERE user_id=$1", u.ID)
	a.logAudit(u.ID, "telegram", "draft_create", commandKind(command), draftID, "success", "", M{"update_id": updateID})
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
	observed, _ := telegramInt(p["observed_cents"])
	mask := str(p, "telegram_mask")
	var heading, details string
	if str(p, "category") == "" {
		heading = "Подтвердите снятие и остаток по карте"
		details = "Карта: " + mask + "\nСумма снятия: " + telegramMoney(amountCents) + "\nОстаток: " + telegramMoney(observed)
	} else {
		heading = "Подтвердите расход по карте"
		details = "Карта: " + mask + "\nСумма расхода: " + telegramMoney(amountCents) + "\nКатегория: " + telegramCategoryName(str(p, "category"))
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
