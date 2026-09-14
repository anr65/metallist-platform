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

func (a *App) telegramSetCommands(chatID int64, role string) error {
	commands := []M{{"command": "start", "description": "Начать работу"}, {"command": "help", "description": "Форматы команд"}, {"command": "withdraw", "description": "Снятие с карты"}, {"command": "expense", "description": "Расход по карте"}}
	if role == "chief" {
		commands = append(commands, M{"command": "balance", "description": "Наблюдаемый остаток карты"})
	}
	return a.telegramCall("setMyCommands", M{"scope": M{"type": "chat", "chat_id": chatID}, "commands": commands})
}

func (a *App) telegramSetupCommands() {
	if telegramToken() == "" || os.Getenv("TELEGRAM_WEBHOOK_SECRET") == "" {
		return
	}
	if e := a.telegramCall("setMyCommands", M{"scope": M{"type": "all_private_chats"}, "commands": []M{{"command": "start", "description": "Начать работу"}, {"command": "help", "description": "Форматы команд"}}}); e != nil {
		log.Printf("Telegram default commands: %v", e)
	}
	rows, e := a.db.Query("SELECT telegram_id,role FROM users WHERE active AND telegram_id IS NOT NULL AND custodian_id IS NOT NULL AND role IN ('collector','chief')")
	if e != nil {
		log.Print("Telegram users: database query failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var chatID int64
		var role string
		if rows.Scan(&chatID, &role) == nil {
			if e := a.telegramSetCommands(chatID, role); e != nil {
				log.Printf("Telegram user commands: %v", e)
			}
		}
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
}
