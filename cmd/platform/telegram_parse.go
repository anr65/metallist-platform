package main

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var telegramLastFour = regexp.MustCompile(`^[0-9]{4}$`)

const telegramBatchLimit = 20

type telegramIntent struct {
	CardID   string
	Mask     string
	Category string
	Amount   int64
	Observed int64
}

func (a *App) telegramCard(u telegramActor, lastFour string) (string, string, error) {
	if !telegramLastFour.MatchString(lastFour) {
		return "", "", errors.New("Укажите ровно последние 4 цифры карты")
	}
	query := "SELECT c.id,c.mask FROM cards c WHERE c.status='active' AND c.last4=$1"
	args := []interface{}{lastFour}
	if u.Role == "collector" {
		query += " AND EXISTS(SELECT 1 FROM card_assignments x WHERE x.card_id=c.id AND x.user_id=$2)"
		args = append(args, u.ID)
	}
	rows, e := a.db.Query(query, args...)
	if e != nil {
		return "", "", errors.New("Не удалось найти карту")
	}
	defer rows.Close()
	var card, mask string
	count := 0
	for rows.Next() {
		count++
		if e = rows.Scan(&card, &mask); e != nil {
			return "", "", errors.New("Не удалось найти карту")
		}
	}
	if e = rows.Err(); e != nil {
		return "", "", errors.New("Не удалось найти карту")
	}
	if count == 0 {
		return "", "", errors.New("Карта с этими четырьмя цифрами вам не назначена или недоступна")
	}
	if count != 1 {
		return "", "", errors.New("Последние четыре цифры неоднозначны. Уточните карту у администратора")
	}
	return card, "****" + mask[len(mask)-4:], nil
}

func (a *App) telegramParse(u telegramActor, command, args string) (telegramIntent, error) {
	words := strings.Fields(args)
	var out telegramIntent
	if command == "withdraw" {
		if len(words) != 2 {
			return out, errors.New("Формат: /withdraw 7898 100к/200")
		}
		parts := strings.Split(words[1], "/")
		if len(parts) != 2 {
			return out, errors.New("После карты укажите сумму снятия и остаток через /, например 100к/200")
		}
		amount, e := telegramAmount(parts[0])
		if e != nil {
			return out, e
		}
		observed, e := nonnegative(parts[1])
		if e != nil {
			return out, errors.New("Некорректный остаток карты")
		}
		out.Amount, out.Observed = amount, observed
	} else if command == "expense" {
		if len(words) < 3 {
			return out, errors.New("Формат: /expense 7898 прогрев 230")
		}
		category, e := telegramCategory(strings.Join(words[1:len(words)-1], " "))
		if e != nil {
			return out, e
		}
		if u.Role == "collector" && category != "warmup" && category != "bank_fee" {
			return out, errors.New("Сборщику доступны только расходы «Прогрев» и «Банк. Комиссия»")
		}
		amount, e := telegramAmount(words[len(words)-1])
		if e != nil {
			return out, e
		}
		out.Amount, out.Category = amount, category
	} else {
		return out, errors.New("Неизвестная команда")
	}
	card, mask, e := a.telegramCard(u, words[0])
	if e != nil {
		return out, e
	}
	out.CardID, out.Mask = card, mask
	return out, nil
}

func (a *App) telegramParseExpenses(u telegramActor, args string) ([]telegramIntent, error) {
	parts := telegramBatchParts(args)
	if len(parts) == 0 {
		return nil, errors.New("Добавьте хотя бы один расход")
	}
	if len(parts) > telegramBatchLimit {
		return nil, fmt.Errorf("За один раз можно добавить не более %d расходов", telegramBatchLimit)
	}
	intents := make([]telegramIntent, 0, len(parts))
	for i, part := range parts {
		intent, err := a.telegramParse(u, "expense", strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("Строка %d: %w", i+1, err)
		}
		intents = append(intents, intent)
	}
	return intents, nil
}

func (a *App) telegramParseWithdrawals(u telegramActor, args string) ([]telegramIntent, error) {
	parts := telegramBatchParts(args)
	if len(parts) == 0 {
		return nil, errors.New("Добавьте хотя бы одно снятие")
	}
	if len(parts) > telegramBatchLimit {
		return nil, fmt.Errorf("За один раз можно добавить не более %d снятий", telegramBatchLimit)
	}
	intents := make([]telegramIntent, 0, len(parts))
	for i, part := range parts {
		intent, err := a.telegramParse(u, "withdraw", strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("Строка %d: %w", i+1, err)
		}
		intents = append(intents, intent)
	}
	return intents, nil
}

func telegramBatchParts(args string) []string {
	return strings.FieldsFunc(strings.ReplaceAll(args, "\r\n", "\n"), func(r rune) bool {
		return r == '\n' || r == ';'
	})
}

func telegramAmount(raw string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	multiplier := int64(1)
	if strings.HasSuffix(s, "к") || strings.HasSuffix(s, "k") {
		multiplier = 1000
		s = strings.TrimSuffix(strings.TrimSuffix(s, "к"), "k")
	}
	v, e := amount(s)
	if e != nil || v > math.MaxInt64/multiplier {
		return 0, errors.New("Некорректная сумма")
	}
	return v * multiplier, nil
}

func telegramCategory(raw string) (string, error) {
	switch strings.ToLower(strings.Join(strings.Fields(raw), " ")) {
	case "прогрев", "warmup":
		return "warmup", nil
	case "банк. комиссия", "банковская комиссия", "банк комиссия", "bank_fee":
		return "bank_fee", nil
	case "агентские", "agent_fee":
		return "agent_fee", nil
	case "зарплата", "salary":
		return "salary", nil
	case "ит инфраструктура", "ит-инфраструктура", "it_infrastructure":
		return "it_infrastructure", nil
	case "налоги", "taxes":
		return "taxes", nil
	case "связь", "communication":
		return "communication", nil
	case "доставка", "delivery":
		return "delivery", nil
	case "прочие расходы", "other":
		return "other", nil
	default:
		return "", errors.New("Неизвестная категория расхода")
	}
}

func telegramCategoryName(code string) string {
	switch code {
	case "warmup":
		return "Прогрев"
	case "bank_fee":
		return "Банк. Комиссия"
	case "agent_fee":
		return "Агентские"
	case "salary":
		return "Зарплата"
	case "it_infrastructure":
		return "ИТ Инфраструктура"
	case "taxes":
		return "Налоги"
	case "communication":
		return "Связь"
	case "delivery":
		return "Доставка"
	case "other":
		return "Прочие РАСХОДЫ"
	default:
		return "Расход"
	}
}

func telegramMoney(cents int64) string {
	whole := fmt.Sprintf("%d", cents/100)
	var groups []string
	for len(whole) > 3 {
		groups = append([]string{whole[len(whole)-3:]}, groups...)
		whole = whole[:len(whole)-3]
	}
	groups = append([]string{whole}, groups...)
	if cents%100 == 0 {
		return strings.Join(groups, " ") + " ₽"
	}
	return fmt.Sprintf("%s,%02d ₽", strings.Join(groups, " "), cents%100)
}
