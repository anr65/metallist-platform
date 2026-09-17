package main

import (
	"bytes"
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
		OK bool `json:"ok"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&result) != nil || !result.OK {
		return errors.New("Telegram отклонил запрос")
	}
	return nil
}

func (a *App) telegramSetGroupCommands(chatID int64) error {
	commands := []M{{"command": "start", "description": "Начать работу"}, {"command": "help", "description": "Форматы команд"}, {"command": "withdraw", "description": "Снятие с карты"}, {"command": "expense", "description": "Расход по карте"}, {"command": "balance", "description": "Остаток — только главный администратор"}}
	return a.telegramCall("setMyCommands", M{"scope": M{"type": "chat", "chat_id": chatID}, "commands": commands})
}

func (a *App) telegramSetupCommands() {
	if telegramToken() == "" || os.Getenv("TELEGRAM_WEBHOOK_SECRET") == "" {
		return
	}
	url := os.Getenv("TELEGRAM_WEBHOOK_URL")
	if url == "" && os.Getenv("APP_ENV") == "demo" {
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
	if e := a.telegramSetGroupCommands(chatID); e != nil {
		log.Printf("Telegram group commands: %v", e)
	}
}
