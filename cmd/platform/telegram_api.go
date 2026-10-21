package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func telegramToken() string {
	path := os.Getenv("TELEGRAM_BOT_TOKEN_FILE")
	if path == "" {
		return ""
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm()&0007 != 0 {
		return ""
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (a *App) telegramCall(method string, payload M) error {
	token := telegramToken()
	if token == "" {
		return errors.New("Telegram не подключён")
	}
	base := "https://api.telegram.org"
	if os.Getenv("APP_ENV") == "testing" && os.Getenv("TELEGRAM_API_BASE") != "" {
		base = strings.TrimRight(os.Getenv("TELEGRAM_API_BASE"), "/")
	}
	body, _ := json.Marshal(payload)
	request, e := http.NewRequest(http.MethodPost, base+"/bot"+token+"/"+method, bytes.NewReader(body))
	if e != nil {
		return errors.New("Не удалось подготовить запрос к Telegram")
	}
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second}
	response, e := client.Do(request)
	if e != nil {
		return errors.New("Telegram не ответил")
	}
	defer response.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&result) != nil {
		return errors.New("Telegram отклонил запрос")
	}
	// Retried pagination/selection edits may already be visible after the prior
	// webhook response was lost. Telegram reports this harmless case as 400.
	if method == "editMessageText" && response.StatusCode == http.StatusBadRequest && strings.HasPrefix(result.Description, "Bad Request: message is not modified") {
		return nil
	}
	if response.StatusCode != http.StatusOK || !result.OK {
		return errors.New("Telegram отклонил запрос")
	}
	return nil
}

func (a *App) telegramEditMessageText(chatID, messageID int64, text string, markup interface{}) error {
	body := M{"chat_id": chatID, "message_id": messageID, "text": text}
	if markup != nil {
		body["reply_markup"] = markup
	}
	if err := a.telegramCall("editMessageText", body); err != nil {
		return errTelegramDelivery
	}
	return nil
}

func telegramCommands() []M {
	return []M{{"command": "start", "description": "Начать работу"}, {"command": "cards_balance", "description": "Балансы всех карт"}, {"command": "my_balance", "description": "Мой баланс наличных"}, {"command": "expense", "description": "Расход по карте"}, {"command": "withdraw", "description": "Снятие с карты"}, {"command": "transfer", "description": "Перевод другому сборщику"}, {"command": "handover", "description": "Передача главному (только Тони)"}, {"command": "card_status", "description": "Изменить статус карты"}, {"command": "block_card", "description": "Заблокировать карту"}, {"command": "cancel", "description": "Отменить текущий ввод"}}
}

func (a *App) telegramSetGroupCommands(chatID int64) error {
	return a.telegramCall("setMyCommands", M{"scope": M{"type": "chat", "chat_id": chatID}, "commands": telegramCommands()})
}

func (a *App) telegramSetAllGroupCommands() error {
	return a.telegramCall("setMyCommands", M{"scope": M{"type": "all_group_chats"}, "commands": telegramCommands()})
}

func (a *App) telegramSetupCommands() {
	if telegramToken() == "" || os.Getenv("TELEGRAM_WEBHOOK_SECRET") == "" {
		return
	}
	url := os.Getenv("TELEGRAM_WEBHOOK_URL")
	if url == "" && os.Getenv("APP_ENV") == "production" {
		url = "https://metallcash.work/telegram/webhook"
	}
	if url != "" {
		if e := a.telegramCall("setWebhook", M{"url": url, "secret_token": os.Getenv("TELEGRAM_WEBHOOK_SECRET"), "allowed_updates": []string{"message", "callback_query"}}); e != nil {
			log.Printf("Telegram webhook setup: %v", e)
		}
	}

	chatID, ok := telegramAllowedGroup()
	if !ok {
		log.Print("Telegram group: TELEGRAM_ALLOWED_CHAT_ID is missing or invalid")
		return
	}
	if e := a.telegramSetAllGroupCommands(); e != nil {
		log.Printf("Telegram all-group commands: %v", e)
	}
	if e := a.telegramSetGroupCommands(chatID); e != nil {
		log.Printf("Telegram group commands: %v", e)
	}
	if e := a.telegramCall("setMyCommands", M{"scope": M{"type": "all_private_chats"}, "commands": []M{{"command": "start", "description": "Подключить личные подтверждения"}, {"command": "pending", "description": "Ожидающие переводы"}}}); e != nil {
		log.Printf("Telegram private commands: %v", e)
	}
	if e := a.telegramSetRepaymentCommands(); e != nil {
		log.Printf("Telegram Tony private commands: %v", e)
	}
}

func (a *App) telegramSetRepaymentCommands() error {
	var chat int64
	err := a.db.QueryRow(`SELECT u.telegram_id FROM users u JOIN custodians c ON c.id=u.custodian_id
 WHERE c.id=$1 AND c.active AND c.kind='collector' AND u.active AND u.role='collector' AND u.telegram_id>0`, telegramTonyCustodianID).Scan(&chat)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return a.telegramCall("setMyCommands", M{"scope": M{"type": "chat", "chat_id": chat}, "commands": []M{
		{"command": "start", "description": "Начать работу"}, {"command": "pending", "description": "Ожидающие переводы"},
		{"command": "repay", "description": "Выдача наличных мерчанту"}, {"command": "cancel", "description": "Отменить ввод возврата"},
	}})
}
