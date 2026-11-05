package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// In-memory driver accepts only the audit INSERT emitted by denied access checks.
type dropdownAuditDriver struct{}
type dropdownAuditConn struct{}

func (dropdownAuditDriver) Open(string) (driver.Conn, error) { return dropdownAuditConn{}, nil }
func (dropdownAuditConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected SQL")
}
func (dropdownAuditConn) Close() error              { return nil }
func (dropdownAuditConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (dropdownAuditConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, "INSERT INTO audit_events") {
		return nil, errors.New("unexpected SQL")
	}
	return driver.RowsAffected(1), nil
}
func init() { sql.Register("dropdown-audit-memory", dropdownAuditDriver{}) }

func TestDropdownChoicesSearchEntireSet(t *testing.T) {
	options := make([]dropdownOption, 160)
	for i := range options {
		options[i] = dropdownOption{Value: string(rune(1000 + i)), Label: "Другой вариант"}
	}
	options[159] = dropdownOption{Value: "last", Label: "СБОРЩИК Ёлкин"}
	body, _ := json.Marshal(dropdownSearchInput{Query: " ёЛКИН ", Options: options})
	recorder := httptest.NewRecorder()
	(&App{}).dropdownSearch(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))), User{Role: "chief"})
	if recorder.Code != 200 {
		t.Fatal(recorder.Body.String())
	}
	var result struct {
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Values) != 1 || result.Values[0] != "last" {
		t.Fatalf("search omitted the last option: %+v", result)
	}
}

func TestDropdownSearchEmptyAndMissing(t *testing.T) {
	for _, tc := range []struct {
		q     string
		count int
	}{{"", 2}, {"нет совпадения", 0}} {
		body, _ := json.Marshal(dropdownSearchInput{Query: tc.q, Options: []dropdownOption{{Value: "a", Label: "Банк"}, {Value: "b", Label: "Карта"}}})
		w := httptest.NewRecorder()
		(&App{}).dropdownSearch(w, httptest.NewRequest("POST", "/", strings.NewReader(string(body))), User{})
		var result struct {
			Values []string `json:"values"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(result.Values) != tc.count {
			t.Fatalf("query %q: %d %s", tc.q, w.Code, w.Body.String())
		}
	}
}

func TestDropdownSearchRejectsInvalidInputAndPreservesSourcePermissions(t *testing.T) {
	cases := []struct {
		method, body string
		status       int
	}{
		{"GET", `{}`, 405},
		{"POST", `{"source":"catalog/users"}`, 400},
		{"POST", `{"source":"unknown"}`, 400},
		{"POST", `{"source":"registry-candidates","context":"invalid"}`, 400},
		{"POST", `{"source":"manual-cards"}`, 403},
		{"POST", `{"source":"requests"}`, 403},
		{"POST", `{"unknown":true}`, 400},
		{"POST", `{"q":"` + strings.Repeat("я", 201) + `"}`, 400},
	}
	db, err := sql.Open("dropdown-audit-memory", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range cases {
		w := httptest.NewRecorder()
		// In-memory driver cannot read domain data or reach a database.
		(&App{db: db}).dropdownSearch(w, httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body)), User{Role: "collector"})
		if w.Code != tc.status {
			t.Fatalf("%s: want %d, got %d %s", tc.body, tc.status, w.Code, w.Body.String())
		}
	}
}

func TestDropdownEntitySearchPreservesScopeAndDoesNotSearchSensitiveFields(t *testing.T) {
	records := []M{
		{"id": "a", "name": "Первый банк", "mask": "•• 1234", "owner_label": "Иван", "pan": "secret-5678", "balance": "987654"},
		{"id": "b", "name": "Другой банк", "mask": "•• 1234"},
		{"id": "outside", "name": "Иван"},
	}
	options := []dropdownOption{{Value: "a", Label: "Карта"}, {Value: "forged", Label: "Иван"}, {Value: "all", Label: "Все"}}
	for _, tc := range []struct {
		q    string
		want string
	}{{"иван", "a"}, {"1234", "a"}, {"5678", ""}, {"987654", ""}, {"Все", "all"}} {
		values := dropdownValues(dropdownSearchInput{Source: "catalog/cards", Query: tc.q, Options: options}, records)
		if len(values) == 0 && tc.want == "" {
			continue
		}
		if len(values) != 1 || values[0] != tc.want {
			t.Fatalf("query %q: got %v, want %q", tc.q, values, tc.want)
		}
	}
}
