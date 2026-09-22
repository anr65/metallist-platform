package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPublicError(t *testing.T) {
	cases := []struct {
		status int
		err    error
		code   string
		text   string
	}{
		{409, errors.New("сумма превышает остаток"), "INSUFFICIENT_BALANCE", "Недостаточно денег"},
		{400, errors.New("некорректная сумма"), "INVALID_AMOUNT", "корректную сумму"},
		{409, errors.New("устаревший предпросмотр"), "STALE_PREVIEW", "Обновите страницу"},
		{409, &pgconn.PgError{Code: "23505", Message: "duplicate key value, secret"}, "ALREADY_EXISTS", "уже существует"},
		{500, errors.New("connection password leaked"), "INTERNAL_ERROR", "внутренней ошибки"},
	}
	for _, tc := range cases {
		got := publicError(tc.status, tc.err)
		if got.code != tc.code || !strings.Contains(got.message, tc.text) || strings.Contains(got.message, "secret") || strings.Contains(got.message, "password") {
			t.Errorf("status %d: got %#v", tc.status, got)
		}
	}
}

func TestFailJSON(t *testing.T) {
	w := httptest.NewRecorder()
	fail(w, 409, errors.New("сумма превышает остаток"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"INSUFFICIENT_BALANCE"`) || !strings.Contains(w.Body.String(), `"error":"Недостаточно денег`) {
		t.Fatalf("unexpected API response: %d %s", w.Code, w.Body.String())
	}
}
