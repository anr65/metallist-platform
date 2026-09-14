package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		Text string `json:"text"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		}
		From struct {
			ID int64 `json:"id"`
		}
	} `json:"message"`
}

func (a *App) telegram(w http.ResponseWriter, r *http.Request) {
	secret := os.Getenv("TELEGRAM_WEBHOOK_SECRET")
	bot := os.Getenv("TELEGRAM_BOT_TOKEN")
	if secret == "" || bot == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != "POST" || r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != secret {
		http.Error(w, "forbidden", 403)
		return
	}
	var x telegramUpdate
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&x); e != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if x.Message.Chat.Type != "private" {
		w.WriteHeader(200)
		return
	}
	res, e := a.db.Exec("INSERT INTO telegram_updates(update_id) VALUES($1) ON CONFLICT DO NOTHING", x.UpdateID)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		w.WriteHeader(200)
		return
	}
	var u User
	e = a.db.QueryRow("SELECT id,login,name,role FROM users WHERE telegram_id=$1 AND active", x.Message.From.ID).Scan(&u.ID, &u.Login, &u.Name, &u.Role)
	if e != nil {
		a.telegramReply(bot, x.Message.Chat.ID, "Доступ не настроен. Обратитесь к администратору.")
		w.WriteHeader(200)
		return
	}
	if u.Role != "operator" && u.Role != "chief" {
		a.telegramReply(bot, x.Message.Chat.ID, "Нет прав на ввод")
		w.WriteHeader(200)
		return
	}
	words := strings.Fields(x.Message.Text)
	if len(words) == 0 {
		w.WriteHeader(200)
		return
	}
	kind := ""
	m := M{}
	switch words[0] {
	case "/withdraw":
		if len(words) != 4 {
			e = errors.New("Формат: /withdraw <ID-карты> <ID-сборщика> <сумма-руб>")
		} else {
			kind = "withdrawal"
			m["card_id"] = words[1]
			m["custodian_id"] = words[2]
			m["amount"] = words[3]
		}
	case "/balance":
		if u.Role != "chief" {
			e = errors.New("Ввод остатков доступен только главному администратору")
		} else if len(words) != 3 {
			e = errors.New("Формат: /balance <ID-карты> <остаток-руб>")
		} else {
			v, err := nonnegative(words[2])
			e = err
			if e == nil && !a.cardAllowed(u, words[1]) {
				e = errors.New("карта не назначена")
			}
			if e == nil {
				_, e = a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,$3,$4,$5,'telegram')", id(), words[1], v, time.Now(), u.ID)
			}
		}
	case "/expense":
		if u.Role != "chief" {
			e = errors.New("Расходы доступны только главному администратору")
		} else if len(words) != 5 {
			e = errors.New("Формат: /expense <card|cash> <ID-источника> <категория> <сумма-руб>")
		} else {
			kind = "expense"
			m["source_kind"] = words[1]
			m["source_id"] = words[2]
			m["category"] = words[3]
			m["amount"] = words[4]
		}
	default:
		e = errors.New("Команды: /withdraw; /balance и /expense — только для главного администратора")
	}
	if kind == "withdrawal" && !a.cardAllowed(u, str(m, "card_id")) {
		e = errors.New("карта не назначена")
	}
	if kind == "expense" && str(m, "source_kind") == "card" && !a.cardAllowed(u, str(m, "source_id")) {
		e = errors.New("карта не назначена")
	}
	if e != nil {
		a.telegramReply(bot, x.Message.Chat.ID, e.Error())
		w.WriteHeader(200)
		return
	}
	if kind != "" {
		v, err := amount(str(m, "amount"))
		if err != nil {
			a.telegramReply(bot, x.Message.Chat.ID, err.Error())
			w.WriteHeader(200)
			return
		}
		m["kind"] = kind
		m["amount_cents"] = v
		newID := id()
		_, e = a.db.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,$2,$3,$4,$5)", newID, kind, encode(m), u.ID, fmt.Sprintf("telegram:%d", x.UpdateID))
		if e != nil {
			a.telegramReply(bot, x.Message.Chat.ID, "Черновик не сохранён; проверьте идентификаторы.")
		} else {
			a.logAudit(u.ID, "telegram", "draft_create", kind, newID, "success", "", M{"update_id": x.UpdateID})
			a.telegramReply(bot, x.Message.Chat.ID, "Черновик "+newID+" создан на "+rub(v)+" ₽. Главный администратор должен явно подтвердить сумму в кабинете.")
		}
	} else {
		a.telegramReply(bot, x.Message.Chat.ID, "Наблюдение остатка сохранено. Ledger не изменён.")
	}
	w.WriteHeader(200)
}
func (a *App) telegramReply(bot string, chat int64, message string) {
	body, _ := json.Marshal(M{"chat_id": chat, "text": message})
	request, _ := http.NewRequest("POST", "https://api.telegram.org/bot"+bot+"/sendMessage", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second}
	response, e := client.Do(request)
	if e == nil {
		response.Body.Close()
	}
}
