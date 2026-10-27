package main

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var telegramCompactFee = regexp.MustCompile(`(?i)комса([0-9])`)

var telegramCardSuffix = regexp.MustCompile(`^(?:[0-9]{4}|[0-9]{6})$`)

const telegramBatchLimit = 20

type telegramIntent struct {
	CardID   string
	Mask     string
	Category string
	Amount   int64
	Observed int64
	BankFee  int64
}

func (a *App) telegramCard(_ telegramActor, suffix string) (string, string, error) {
	if !telegramCardSuffix.MatchString(suffix) {
		return "", "", errors.New("Укажите последние 4 или 6 цифр карты")
	}
	lastFour := suffix[len(suffix)-4:]
	rows, e := a.db.Query("SELECT c.id,c.mask,c.pan_ciphertext FROM cards c WHERE c.status='active' AND c.last4=$1", lastFour)
	if e != nil {
		return "", "", errors.New("Не удалось найти карту")
	}
	defer rows.Close()
	var card, mask string
	count := 0
	for rows.Next() {
		var candidate, candidateMask string
		var ciphertext []byte
		if e = rows.Scan(&candidate, &candidateMask, &ciphertext); e != nil {
			return "", "", errors.New("Не удалось найти карту")
		}
		if len(suffix) == 6 {
			pan, err := readCardPAN(candidate, ciphertext)
			if err != nil {
				// Do not select another candidate when this card cannot be checked.
				return "", "", errors.New("Не удалось проверить последние 6 цифр карты. Обратитесь к администратору")
			}
			if !strings.HasSuffix(pan, suffix) {
				continue
			}
		}
		count++
		card, mask = candidate, candidateMask
	}
	if e = rows.Err(); e != nil {
		return "", "", errors.New("Не удалось найти карту")
	}
	if count == 0 {
		return "", "", errors.New("Активная карта с этими цифрами не найдена")
	}
	if count != 1 {
		if len(suffix) == 4 {
			return "", "", errors.New("Последние четыре цифры неоднозначны: существует другая активная карта с таким же окончанием номера. Введите последние 6 цифр нужной карты вместо 4 и повторите весь пакет. Этот пакет не принят")
		}
		return "", "", errors.New("Последние 6 цифр совпадают у нескольких активных карт. Уточните карту у администратора. Этот пакет не принят")
	}
	return card, "****" + mask[len(mask)-4:], nil
}

func (a *App) telegramParse(u telegramActor, command, args string) (telegramIntent, error) {
	words := strings.Fields(telegramCompactFee.ReplaceAllString(args, "комса $1"))
	var out telegramIntent
	if command == "withdraw" {
		if len(words) == 2 && telegramCardSuffix.MatchString(words[0]) {
			if _, ok := cardStatusLabels[normalizeCardStatus(words[1])]; ok {
				return out, telegramFieldError(command, "operation", "wrong_operation", "В строке указан статус карты. Уберите её из снятий и отправьте /card_status "+words[0]+" "+words[1]+". Весь пакет не принят")
			}
		}
		if len(words) != 2 && len(words) != 4 {
			return out, telegramFieldError(command, "format", "invalid_format", "Без комиссии: 0000 100к/200. С комиссией: 0000 100к/200 комса 2400")
		}
		parts := strings.Split(words[1], "/")
		if len(parts) != 2 {
			return out, telegramFieldError(command, "format", "invalid_format", "После карты укажите сумму снятия и остаток через /: 0000 100к/200 или 0000 100к/200 комса 2400")
		}
		amount, e := telegramAmount(parts[0])
		if e != nil {
			return out, telegramFieldError(command, "amount", "invalid_amount", "Некорректная сумма: укажите положительную сумму, например 100000, 100к или 25,50")
		}
		observed, e := telegramObservedAmount(parts[1])
		if e != nil {
			return out, telegramFieldError(command, "balance", "invalid_balance", "Некорректный остаток карты: укажите ноль или положительную сумму после /, например 0000 100к/200")
		}
		out.Amount, out.Observed = amount, observed
		if len(words) == 4 {
			if !strings.EqualFold(words[2], "комса") {
				return out, telegramFieldError(command, "fee", "invalid_fee", "После остатка укажите комиссию: 0000 100к/200 комса 2400")
			}
			fee, err := telegramAmount(words[3])
			if err != nil || fee <= 0 || fee > math.MaxInt64-amount {
				return out, telegramFieldError(command, "fee", "invalid_fee", "Некорректная сумма банковской комиссии. Пример: 0000 100к/200 комса 2400")
			}
			out.BankFee = fee
		}
	} else if command == "expense" {
		if len(words) < 3 {
			return out, telegramFieldError(command, "format", "invalid_format", "Введите карту, категорию и сумму: 0000 прогрев 230 или 0000 комса 25,50")
		}
		category, e := telegramCategory(strings.Join(words[1:len(words)-1], " "))
		if e != nil {
			return out, telegramFieldError(command, "category", "invalid_category", e.Error())
		}
		if telegramFieldRole(u.Role) && category != "warmup" && category != "bank_fee" {
			return out, errors.New("Доступны только расходы «Прогрев» и «Комса»")
		}
		amount, e := telegramAmount(words[len(words)-1])
		if e != nil {
			return out, telegramFieldError(command, "amount", "invalid_amount", "Некорректная сумма расхода: укажите положительную сумму, например 230 или 25,50")
		}
		out.Amount, out.Category = amount, category
	} else {
		return out, errors.New("Неизвестная команда")
	}
	card, mask, e := a.telegramCard(u, words[0])
	if e != nil {
		return out, telegramFieldError(command, "card", "invalid_card", e.Error())
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
			return nil, telegramRowInputError("expense", i+1, err)
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
			return nil, telegramRowInputError("withdraw", i+1, err)
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

func telegramObservedAmount(raw string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	multiplier := int64(1)
	if strings.HasSuffix(s, "к") || strings.HasSuffix(s, "k") {
		multiplier = 1000
		s = strings.TrimSuffix(strings.TrimSuffix(s, "к"), "k")
	}
	v, e := nonnegative(s)
	if e != nil || v > math.MaxInt64/multiplier {
		return 0, errors.New("некорректный остаток")
	}
	return v * multiplier, nil
}

func telegramCategory(raw string) (string, error) {
	switch strings.ToLower(strings.Join(strings.Fields(raw), " ")) {
	case "прогрев", "warmup":
		return "warmup", nil
	case "комса", "bank_fee":
		return "bank_fee", nil
	case "агентские", "agent_fee":
		return "agent_fee", nil
	case "зарплата", "salary":
		return "salary", nil
	case "ит инфраструктура", "ит-инфраструктура", "it_infrastructure":
		return "it_infrastructure", nil
	case "налоги", "taxes":
		return "taxes", nil
	case "такси", "taxi":
		return "taxi", nil
	case "материал", "material":
		return "material", nil
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
	case "taxi":
		return "Такси"
	case "material":
		return "Материал"
	case "communication":
		return "Связь"
	case "delivery":
		return "Доставка"
	case "other":
		return "Прочие расходы"
	default:
		return "Расход"
	}
}

func telegramMoney(cents int64) string {
	sign := ""
	magnitude := uint64(cents)
	if cents < 0 {
		sign = "-"
		magnitude = uint64(-(cents + 1)) + 1
	}
	whole := fmt.Sprintf("%d", magnitude/100)
	var groups []string
	for len(whole) > 3 {
		groups = append([]string{whole[len(whole)-3:]}, groups...)
		whole = whole[:len(whole)-3]
	}
	groups = append([]string{whole}, groups...)
	if magnitude%100 == 0 {
		return sign + strings.Join(groups, " ") + " ₽"
	}
	return fmt.Sprintf("%s%s,%02d ₽", sign, strings.Join(groups, " "), magnitude%100)
}

// Salary uses the author's cash, never a card or a caller-supplied custodian.
func telegramSalary(args, custodian string) (M, error) {
	words := strings.Fields(args)
	if len(words) < 2 {
		return nil, telegramFieldError("salary", "comment", "required_comment", "Введите зарплату и обязательный комментарий: зп 50000 за октябрь")
	}
	cents, err := telegramAmount(words[0])
	if err != nil || cents <= 0 {
		return nil, telegramFieldError("salary", "amount", "invalid_amount", "Укажите положительную сумму зарплаты: зп 50000 за октябрь")
	}
	comment := strings.TrimSpace(strings.TrimSpace(args)[len(words[0]):])
	return M{"source_kind": "cash", "source_id": custodian, "category": "salary", "amount": rub(cents), "amount_cents": cents, "comment": comment}, nil
}
