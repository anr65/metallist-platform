package main

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
)

// API error codes are stable identifiers for the interface and integrations.
// Messages are the single source of user-facing Russian copy.
type errorDefinition struct {
	code, message string
}

var errorCatalog = map[string]errorDefinition{
	"сумма превышает остаток":                 {"INSUFFICIENT_BALANCE", "Недостаточно денег у выбранного ответственного или на карте. Укажите сумму в пределах учётного остатка."},
	"недостаточно денег в источнике":          {"INSUFFICIENT_BALANCE", "Недостаточно денег в выбранном источнике. Укажите сумму в пределах учётного остатка."},
	"устаревший предпросмотр":                 {"STALE_PREVIEW", "Данные изменились. Обновите страницу и повторите действие."},
	"подтвердите точную сумму":                {"AMOUNT_MISMATCH", "Подтвердите точную сумму операции."},
	"некорректная сумма":                      {"INVALID_AMOUNT", "Укажите корректную сумму в рублях."},
	"только копейки":                          {"INVALID_AMOUNT", "Укажите не более двух знаков после запятой."},
	"сумма должна быть положительной":         {"INVALID_AMOUNT", "Сумма должна быть больше нуля."},
	"недостаточно прав":                       {"FORBIDDEN", "У вас нет прав для этого действия."},
	"только главный администратор":            {"FORBIDDEN", "Это действие доступно только главному администратору."},
	"карта не назначена":                      {"CARD_NOT_ASSIGNED", "Эта карта вам не назначена."},
	"передача только главному администратору": {"INVALID_RECIPIENT", "Получателем передачи должен быть главный администратор."},
	"для мерчанта не задан действующий тариф; утвердите общий тариф или разовую ставку реестра": {"MISSING_TARIFF", "Для мерчанта не задан действующий тариф; утвердите общий тариф или разовую ставку реестра."},
	"запрос уже изменён; откройте его заново":                                                   {"STALE_DATA", "Запрос изменился. Откройте его заново."},
	"маска карты изменилась; обновите список и повторите действие":                              {"STALE_DATA", "Данные карты изменились. Обновите список и повторите действие."},
	"неверный текущий пароль":                                                                   {"INVALID_CREDENTIALS", "Текущий пароль неверен."},
	"неверный вход":             {"INVALID_CREDENTIALS", "Неверный логин или пароль."},
	"колонки PIN/CVV запрещены": {"UNSAFE_FILE", "Удалите из файла колонки PIN и CVV и загрузите его снова."},
	"макросы запрещены":         {"UNSAFE_FILE", "Файлы с макросами не принимаются. Удалите макросы и загрузите файл снова."},
}

func publicError(status int, err error) errorDefinition {
	var dbError *pgconn.PgError
	if errors.As(err, &dbError) {
		switch dbError.Code {
		case "23505":
			return errorDefinition{"ALREADY_EXISTS", "Такая запись уже существует. Проверьте данные и обновите страницу."}
		case "23503":
			return errorDefinition{"RELATED_RECORD", "Связанная запись не найдена или уже изменена. Обновите страницу."}
		default:
			return errorDefinition{"DATABASE_ERROR", "Не удалось сохранить данные. Повторите попытку позже."}
		}
	}
	if err != nil {
		if known, ok := errorCatalog[err.Error()]; ok {
			return known
		}
	}
	switch {
	case status >= http.StatusInternalServerError:
		return errorDefinition{"INTERNAL_ERROR", "Не удалось выполнить действие из-за внутренней ошибки. Повторите позже."}
	case status == http.StatusUnauthorized:
		return errorDefinition{"UNAUTHORIZED", "Сеанс завершён. Войдите снова."}
	case status == http.StatusForbidden:
		return errorDefinition{"FORBIDDEN", "У вас нет прав для этого действия."}
	case status == http.StatusNotFound:
		return errorDefinition{"NOT_FOUND", "Запись не найдена. Обновите страницу."}
	case status == http.StatusConflict:
		if safeBusinessMessage(err) {
			return errorDefinition{"CONFLICT", err.Error()}
		}
		return errorDefinition{"CONFLICT", "Данные изменились или действие уже выполнено. Обновите страницу и повторите попытку."}
	default:
		if safeBusinessMessage(err) {
			return errorDefinition{"VALIDATION_ERROR", err.Error()}
		}
		return errorDefinition{"BAD_REQUEST", "Проверьте введённые данные и повторите попытку."}
	}
}

func safeBusinessMessage(err error) bool {
	if err == nil || len(err.Error()) > 300 || strings.ContainsAny(err.Error(), "\n\r\t") {
		return false
	}
	for _, r := range err.Error() {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func fail(w http.ResponseWriter, status int, err error) {
	if err == nil {
		err = errors.New("unknown error")
	}
	public := publicError(status, err)
	var dbError *pgconn.PgError
	if errors.As(err, &dbError) {
		log.Printf("API %s: PostgreSQL SQLSTATE %s", public.code, dbError.Code)
	} else if status >= http.StatusInternalServerError {
		log.Printf("API %s: %T", public.code, err)
	}
	respond(w, status, M{"code": public.code, "error": public.message})
}
